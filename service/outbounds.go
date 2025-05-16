package service

import (
	"encoding/json"
	"os"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/logger"
	"s-ui/util/common"

	"gorm.io/gorm"
)

type OutboundService struct{}

func (o *OutboundService) GetAll() (*[]map[string]interface{}, error) {
	db := database.GetDB()
	outbounds := []*model.Outbound{}
	err := db.Model(&model.Outbound{}).Find(&outbounds).Error // Corrected: Use Find and pass pointer to slice
	if err != nil {
		return nil, common.NewErrorf("failed to get all outbounds: %w", err)
	}
	var data []map[string]interface{}
	for _, outbound := range outbounds {
		outData := map[string]interface{}{
			"id":   outbound.Id,
			"type": outbound.Type,
			"tag":  outbound.Tag,
		}
		if outbound.Options != nil {
			var restFields map[string]interface{} // Changed to interface{} for direct assignment
			if err := json.Unmarshal(outbound.Options, &restFields); err != nil {
				// Log error and skip options for this outbound, instead of failing all
				logger.Warning("Failed to unmarshal options for outbound ID ", outbound.Id, ": ", err)
			} else {
				for k, v := range restFields {
					outData[k] = v
				}
			}
		}
		data = append(data, outData)
	}
	return &data, nil
}

func (o *OutboundService) GetAllConfig(db *gorm.DB) ([]json.RawMessage, error) {
	var outboundsJson []json.RawMessage
	var outbounds []*model.Outbound
	err := db.Model(&model.Outbound{}).Find(&outbounds).Error // Corrected: Use Find and pass pointer to slice
	if err != nil {
		return nil, common.NewErrorf("failed to get all outbound configs: %w", err)
	}
	for _, outbound := range outbounds {
		outboundJson, err := outbound.MarshalJSON()
		if err != nil {
			// Log error and skip this outbound config, instead of failing all
			logger.Warning("Failed to marshal outbound ID ", outbound.Id, " to JSON: ", err)
			continue
		}
		outboundsJson = append(outboundsJson, outboundJson)
	}
	return outboundsJson, nil
}

func (s *OutboundService) Save(tx *gorm.DB, act string, data json.RawMessage) error {
	var err error

	switch act {
	case "new", "edit":
		var outbound model.Outbound
		err = outbound.UnmarshalJSON(data)
		if err != nil {
			return common.NewErrorf("failed to unmarshal outbound data for save: %w", err)
		}

		// Basic validation
		if outbound.Tag == "" {
			return common.NewError("outbound tag cannot be empty")
		}
		if outbound.Type == "" {
			// Allow type to be empty, sing-box might default it or it might be a type that doesn't need it.
			// If specific types require validation, it should be added here or in a dedicated validation function.
			logger.Debugf("Outbound tag '%s' has an empty type.", outbound.Tag)
		}

		// Check for duplicate tag
		var count int64
		query := tx.Model(&model.Outbound{}).Where("tag = ?", outbound.Tag)
		if act == "edit" {
			query = query.Where("id != ?", outbound.Id)
		}
		err = query.Count(&count).Error
		if err != nil {
			return common.NewErrorf("failed to check for duplicate outbound tag '%s': %w", outbound.Tag, err)
		}
		if count > 0 {
			return common.NewErrorf("outbound tag '%s' already exists", outbound.Tag)
		}

		if corePtr.IsRunning() {
			configData, err := outbound.MarshalJSON()
			if err != nil {
				return common.NewErrorf("failed to marshal outbound for core operation: %w", err)
			}
			if act == "edit" {
				var oldTag string
				// Use Pluck for single field, and handle potential ErrRecordNotFound
				err = tx.Model(&model.Outbound{}).Where("id = ?", outbound.Id).Pluck("tag", &oldTag).Error
				if err != nil {
					if database.IsNotFound(err) {
						logger.Warningf("Outbound with ID %d not found when attempting to get old tag for edit. Proceeding as if it's a new core entry.", outbound.Id)
						// oldTag will be empty, so RemoveOutbound won't be called or will be a no-op if it handles empty string
					} else {
						return common.NewErrorf("failed to get old tag for outbound ID %d: %w", outbound.Id, err)
					}
				}
				if oldTag != "" { // Only remove if oldTag was found
					err = corePtr.RemoveOutbound(oldTag)
					if err != nil && err != os.ErrInvalid { // os.ErrInvalid might mean it wasn't found in core, which is fine
						// Log this error but attempt to add the new one anyway, as removing the old one is best-effort
						logger.Errorf("Failed to remove old outbound '%s' from core: %v. Attempting to add new/updated outbound '%s'.", oldTag, err, outbound.Tag)
					}
				}
			}
			err = corePtr.AddOutbound(configData)
			if err != nil {
				return common.NewErrorf("failed to add outbound '%s' to core: %w", outbound.Tag, err)
			}
		}

		err = tx.Save(&outbound).Error
		if err != nil {
			return common.NewErrorf("failed to save outbound '%s' to database: %w", outbound.Tag, err)
		}
	case "del":
		var tag string
		err = json.Unmarshal(data, &tag)
		if err != nil {
			return common.NewErrorf("failed to unmarshal tag for delete: %w", err)
		}
		if tag == "" {
			return common.NewError("tag for delete cannot be empty")
		}
		if corePtr.IsRunning() {
			err = corePtr.RemoveOutbound(tag)
			if err != nil && err != os.ErrInvalid { // os.ErrInvalid might mean it wasn't found in core, which is fine
				// Log this error but proceed with DB deletion
				logger.Errorf("Failed to remove outbound '%s' from core during delete: %v. Proceeding with database deletion.", tag, err)
			}
		}
		// Ensure we pass a pointer to Delete for proper GORM behavior with struct conditions
		err = tx.Where("tag = ?", tag).Delete(&model.Outbound{}).Error
		if err != nil {
			return common.NewErrorf("failed to delete outbound '%s' from database: %w", tag, err)
		}
	default:
		return common.NewErrorf("unknown action: %s", act)
	}
	return nil
}
