package service

import (
	"encoding/json"
	"log" // Added for logging
	"os"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/util/common"

	"gorm.io/gorm"
)

type EndpointService struct {
	WarpService
}

func (o *EndpointService) GetAll() (*[]map[string]interface{}, error) {
	db := database.GetDB()
	endpoints := []*model.Endpoint{}
	err := db.Model(&model.Endpoint{}).Find(&endpoints).Error
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	for _, endpoint := range endpoints {
		// Initialize epData. Options will be added first, then canonical fields.
		epData := make(map[string]interface{})

		if endpoint.Options != nil {
			var restFields map[string]json.RawMessage
			if err := json.Unmarshal(endpoint.Options, &restFields); err != nil {
				// Log the error for the specific endpoint and continue, or return error.
				// Current choice: Log and skip options for this endpoint to allow others to process.
				log.Printf("Warning: Failed to unmarshal options for endpoint ID %d (Tag: %s): %v. Skipping options for this endpoint.", endpoint.Id, endpoint.Tag, err)
			} else {
				for k, v := range restFields {
					// Check for potential key collisions if specific handling is needed beyond overwrite.
					// For now, options are added first.
					epData[k] = v
				}
			}
		}

		// Set canonical fields. These will overwrite any same-named fields from Options.
		epData["id"] = endpoint.Id
		epData["type"] = endpoint.Type
		epData["tag"] = endpoint.Tag
		epData["ext"] = endpoint.Ext

		data = append(data, epData)
	}
	return &data, nil
}

func (o *EndpointService) GetAllConfig(db *gorm.DB) ([]json.RawMessage, error) {
	var endpointsJson []json.RawMessage
	var endpoints []*model.Endpoint
	err := db.Model(&model.Endpoint{}).Find(&endpoints).Error // Ensure Model is called with a pointer
	if err != nil {
		return nil, err
	}
	for _, endpoint := range endpoints {
		endpointJson, err := endpoint.MarshalJSON()
		if err != nil {
			return nil, err // Return error if marshal fails
		}
		endpointsJson = append(endpointsJson, endpointJson)
	}
	return endpointsJson, nil
}

func (s *EndpointService) Save(tx *gorm.DB, act string, data json.RawMessage) error {
	var err error

	switch act {
	case "new", "edit":
		var endpoint model.Endpoint
		err = endpoint.UnmarshalJSON(data)
		if err != nil {
			return err
		}
		// Basic validation for required fields
		if endpoint.Type == "" {
			return common.NewError("Endpoint type is required.")
		}
		if endpoint.Tag == "" {
			return common.NewError("Endpoint tag is required.")
		}

		var endpointOpts map[string]interface{}
		if endpoint.Type == "wireguard" || endpoint.Type == "warp" {
			if endpoint.Options == nil {
				return common.NewError("Endpoint options are required for wireguard/warp types.")
			}
			if err := json.Unmarshal(endpoint.Options, &endpointOpts); err != nil {
				return common.NewError("Invalid endpoint options JSON.")
			}
		}

		// WireGuard-specific validation
		if endpoint.Type == "wireguard" || endpoint.Type == "warp" {
			// Use endpointOpts directly here
			peers, ok := endpointOpts["peers"].([]interface{})
			if !ok || len(peers) == 0 {
				return common.NewError("WireGuard endpoint must have at least one peer.")
			}
			// Corrected nested loop for persistent_keepalive check
			for i, peerRaw := range peers {
				peer, ok := peerRaw.(map[string]interface{})
				if !ok {
					return common.NewErrorf("Peer %d is not a valid object.", i)
				}
				if pk, ok := peer["public_key"].(string); !ok || pk == "" {
					return common.NewErrorf("Peer %d missing public_key.", i)
				}
				if addr, ok := peer["address"].(string); !ok || addr == "" {
					return common.NewErrorf("Peer %d missing address.", i)
				}
				if port, ok := peer["port"].(float64); !ok || port <= 0 {
					return common.NewErrorf("Peer %d missing or invalid port.", i)
				}
				if allowed, ok := peer["allowed_ips"].([]interface{}); !ok || len(allowed) == 0 {
					return common.NewErrorf("Peer %d missing allowed_ips.", i)
				}
				if _, ok := peer["persistent_keepalive"].(float64); !ok {
					return common.NewErrorf("Peer %d does not have persistent_keepalive set. This may cause NAT issues.", i)
				}
			}
		}

		// Check for duplicate tag on new or if tag changed on edit
		if act == "new" {
			var count int64
			err = tx.Model(&model.Endpoint{}).Where("tag = ?", endpoint.Tag).Count(&count).Error
			if err != nil {
				return err
			}
			if count > 0 {
				return common.NewErrorf("Endpoint tag '%s' already exists.", endpoint.Tag)
			}
		} else if act == "edit" {
			var existingEndpoint model.Endpoint
			if err := tx.Model(&model.Endpoint{}).Where("id = ?", endpoint.Id).First(&existingEndpoint).Error; err != nil {
				return common.NewErrorf("Failed to find existing endpoint with id %d: %v", endpoint.Id, err)
			}
			if existingEndpoint.Tag != endpoint.Tag { // Tag has changed, check for duplicates
				var count int64
				err = tx.Model(&model.Endpoint{}).Where("tag = ? AND id != ?", endpoint.Tag, endpoint.Id).Count(&count).Error
				if err != nil {
					return err
				}
				if count > 0 {
					return common.NewErrorf("Endpoint tag '%s' already exists.", endpoint.Tag)
				}
			}
		}

		// Check for duplicate/conflicting allowed_ips among all endpoints (WireGuard only)
		if endpoint.Type == "wireguard" || endpoint.Type == "warp" {
			newAllowedIPs, err := extractAllowedIPsFromOptions(endpoint.Options)
			if err != nil {
				return common.NewErrorf("Failed to extract allowed IPs from current endpoint's options: %v", err)
			}

			// If there are no new allowed IPs, no need to check for conflicts.
			if len(newAllowedIPs) == 0 {
				// This case should ideally be prevented by earlier validations ensuring peers have allowed_ips.
			} else {
				var allEndpoints []*model.Endpoint
				// Exclude current endpoint if editing
				query := tx.Model(&model.Endpoint{})
				if act == "edit" {
					query = query.Where("id != ?", endpoint.Id)
				}
				err = query.Find(&allEndpoints).Error
				if err != nil {
					return err
				}

				for _, ep := range allEndpoints {
					if ep.Type != "wireguard" && ep.Type != "warp" { // Only check against other WireGuard/Warp endpoints
						continue
					}
					existingAllowedIPs, err := extractAllowedIPsFromOptions(ep.Options)
					if err != nil {
						log.Printf("Warning: Could not extract allowed IPs from existing endpoint %s (ID: %d) during conflict check: %v", ep.Tag, ep.Id, err)
						continue // Skip if options are invalid or IPs can't be extracted
					}

					for _, existingIPStr := range existingAllowedIPs {
						for _, newIP := range newAllowedIPs {
							if newIP == existingIPStr {
								return common.NewErrorf("Allowed IP %s is already used by endpoint tag '%s'.", newIP, ep.Tag)
							}
						}
					}
				}
			}
		}

		// Warp-specific handling
		if endpoint.Type == "warp" {
			if act == "new" {
				err = s.WarpService.RegisterWarp(&endpoint)
				if err != nil {
					return err
				}
			} else { // "edit"
				var oldLicense string
				// Use Pluck for single column, ensure it's from the correct endpoint
				err = tx.Model(&model.Endpoint{}).Where("id = ?", endpoint.Id).Pluck("json_extract(ext, '$.license_key')", &oldLicense).Error
				if err != nil {
					if err == gorm.ErrRecordNotFound {
						oldLicense = ""
					} else {
						return common.NewErrorf("Failed to retrieve old license key for endpoint ID %d: %v", endpoint.Id, err)
					}
				}
				err = s.WarpService.SetWarpLicense(oldLicense, &endpoint)
				if err != nil {
					return common.NewErrorf("Failed to set Warp license for endpoint ID %d: %v", endpoint.Id, err)
				}
			}
		}

		if corePtr.IsRunning() {
			configData, err := endpoint.MarshalJSON()
			if err != nil {
				return err
			}
			if act == "edit" {
				var oldTag string
				var oldType string // Added to fetch the old type

				// Fetch oldTag
				err = tx.Model(&model.Endpoint{}).Where("id = ?", endpoint.Id).Pluck("tag", &oldTag).Error
				if err != nil {
					if err == gorm.ErrRecordNotFound {
						oldTag = "" // If not found, assume it wasn't in core or tag was cleared
					} else {
						return common.NewErrorf("Failed to retrieve old tag for endpoint ID %d: %v", endpoint.Id, err)
					}
				}

				// Fetch oldType
				err = tx.Model(&model.Endpoint{}).Where("id = ?", endpoint.Id).Pluck("type", &oldType).Error
				if err != nil {
					if err == gorm.ErrRecordNotFound {
						// This is less likely if oldTag was found, but handle defensively
						oldType = ""
					} else {
						return common.NewErrorf("Failed to retrieve old type for endpoint ID %d: %v", endpoint.Id, err)
					}
				}

				// Remove from core if oldTag exists AND (tag has changed OR type has changed)
				if oldTag != "" && (oldTag != endpoint.Tag || oldType != endpoint.Type) {
					err = corePtr.RemoveEndpoint(oldTag)    // Remove using the OLD tag
					if err != nil && err != os.ErrInvalid { // os.ErrInvalid might mean tag not found, which is fine
						return common.NewErrorf("Failed to remove old endpoint '%s' (type: '%s') from core: %v", oldTag, oldType, err)
					}
				}
			}
			err = corePtr.AddEndpoint(configData) // Add/update with new config
			if err != nil {
				return common.NewErrorf("Failed to add/update endpoint '%s' (type: '%s') in core: %v", endpoint.Tag, endpoint.Type, err)
			}
		}

		err = tx.Save(&endpoint).Error
		if err != nil {
			return err
		}
	case "del":
		var tag string
		err = json.Unmarshal(data, &tag)
		if err != nil {
			return err
		}
		if corePtr.IsRunning() {
			err = corePtr.RemoveEndpoint(tag)
			if err != nil && err != os.ErrInvalid {
				return err
			}
		}
		err = tx.Where("tag = ?", tag).Delete(&model.Endpoint{}).Error // Pass pointer to Delete
		if err != nil {
			return err
		}
	default:
		return common.NewErrorf("unknown action: %s", act)
	}
	return nil
}

// Helper function to extract all allowed_ips from an endpoint's options
func extractAllowedIPsFromOptions(options json.RawMessage) ([]string, error) {
	if options == nil {
		return []string{}, nil
	}

	var optsData map[string]interface{}
	if err := json.Unmarshal(options, &optsData); err != nil {
		return nil, common.NewErrorf("invalid endpoint options JSON for IP extraction: %v", err)
	}

	peersRaw, ok := optsData["peers"]
	if !ok {
		return []string{}, nil // No "peers" key
	}

	peers, ok := peersRaw.([]interface{})
	if !ok {
		return nil, common.NewError("endpoint options 'peers' field is not an array")
	}

	var allAllowedIPs []string
	for i, peerRaw := range peers {
		peer, ok := peerRaw.(map[string]interface{})
		if !ok {
			return nil, common.NewErrorf("peer %d in endpoint options is not a valid object", i)
		}

		allowedIPsRaw, ok := peer["allowed_ips"]
		if !ok {
			continue // Peer has no "allowed_ips" key
		}

		allowedIPsSlice, ok := allowedIPsRaw.([]interface{})
		if !ok {
			return nil, common.NewErrorf("peer %d 'allowed_ips' field is not an array", i)
		}

		for j, ipRaw := range allowedIPsSlice {
			ipStr, ok := ipRaw.(string)
			if !ok {
				return nil, common.NewErrorf("peer %d 'allowed_ips' entry %d is not a string", i, j)
			}
			allAllowedIPs = append(allAllowedIPs, ipStr)
		}
	}
	return allAllowedIPs, nil
}
