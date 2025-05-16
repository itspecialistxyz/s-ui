package service

import (
	"encoding/json"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/logger"
	"s-ui/util"
	"s-ui/util/common"
	"strings"
	"time"

	"gorm.io/gorm"
)

type ClientService struct {
	InboundService
}

func (s *ClientService) Get(id string) (*[]model.Client, error) {
	if id == "" {
		return s.GetAll()
	}
	return s.getById(id)
}

func (s *ClientService) getById(id string) (*[]model.Client, error) {
	db := database.GetDB()
	var client []model.Client
	err := db.Model(model.Client{}).Where("id in ?", strings.Split(id, ",")).Find(&client).Error
	if err != nil {
		return nil, err
	}

	return &client, nil
}

func (s *ClientService) GetAll() (*[]model.Client, error) {
	db := database.GetDB()
	var clients []model.Client
	err := db.Model(model.Client{}).Select("`id`, `enable`, `name`, `desc`, `group`, `inbounds`, `up`, `down`, `volume`, `expiry`").Find(&clients).Error
	if err != nil {
		return nil, err
	}
	return &clients, nil
}

func (s *ClientService) Save(tx *gorm.DB, act string, data json.RawMessage, hostname string) ([]uint, error) {
	var err error
	var inboundIds []uint

	switch act {
	case "new", "edit":
		var client model.Client
		err = json.Unmarshal(data, &client)
		if err != nil {
			return nil, common.NewErrorf("failed to unmarshal client data: %w", err)
		}
		err = json.Unmarshal(client.Inbounds, &inboundIds)
		if err != nil {
			return nil, common.NewErrorf("failed to unmarshal client.Inbounds for client ID %d: %w", client.Id, err)
		}
		err = s.updateLinksWithFixedInbounds(tx, []*model.Client{&client}, inboundIds, hostname)
		if err != nil {
			return nil, err
		}
		err = tx.Save(&client).Error
		if err != nil {
			return nil, common.NewErrorf("failed to save client ID %d: %w", client.Id, err)
		}
	case "addbulk":
		var clients []*model.Client
		err = json.Unmarshal(data, &clients)
		if err != nil {
			return nil, common.NewErrorf("failed to unmarshal bulk client data: %w", err)
		}
		if len(clients) == 0 {
			// No clients to add. Return successfully with no inbound IDs affected.
			return []uint{}, nil
		}
		// Assuming all clients in the bulk operation share the same inbounds,
		// as defined by the first client.
		// Check if the first client's Inbounds field is nil
		if clients[0].Inbounds != nil {
			err = json.Unmarshal(clients[0].Inbounds, &inboundIds)
			if err != nil {
				return nil, common.NewErrorf("failed to unmarshal inbounds for the first client in bulk add: %w", err)
			}
		} else {
			// If Inbounds is nil, initialize inboundIds as an empty slice.
			// This means no specific inbounds are linked, or they will be handled by updateLinksWithFixedInbounds if logic allows.
			inboundIds = []uint{}
		}
		err = s.updateLinksWithFixedInbounds(tx, clients, inboundIds, hostname)
		if err != nil {
			return nil, common.NewErrorf("failed to update links for bulk clients: %w", err)
		}
		err = tx.Save(clients).Error
		if err != nil {
			return nil, common.NewErrorf("failed to save bulk clients: %w", err)
		}
	case "del":
		var id uint
		err = json.Unmarshal(data, &id)
		if err != nil {
			return nil, common.NewErrorf("failed to unmarshal client ID for deletion: %w", err)
		}
		var client model.Client
		err = tx.Where("id = ?", id).First(&client).Error
		if err != nil {
			return nil, common.NewErrorf("failed to find client ID %d for deletion: %w", id, err)
		}
		err = json.Unmarshal(client.Inbounds, &inboundIds)
		if err != nil {
			return nil, common.NewErrorf("failed to unmarshal client.Inbounds for client ID %d being deleted: %w", id, err)
		}
		err = tx.Where("id = ?", id).Delete(model.Client{}).Error
		if err != nil {
			return nil, common.NewErrorf("failed to delete client ID %d: %w", id, err)
		}
	default:
		return nil, common.NewErrorf("unknown action: %s", act)
	}

	return inboundIds, nil
}

func (s *ClientService) updateLinksWithFixedInbounds(tx *gorm.DB, clients []*model.Client, inbounIds []uint, hostname string) error {
	var err error
	var inbounds []model.Inbound

	// Zero inbounds means removing local links only
	if len(inbounIds) > 0 {
		err = tx.Model(model.Inbound{}).Preload("Tls").Where("id in ? and type in ?", inbounIds, util.InboundTypeWithLink).Find(&inbounds).Error
		if err != nil {
			return common.NewErrorf("failed to find inbounds for link update: %w", err)
		}
	}
	for index, client := range clients {
		var clientLinks []map[string]string
		// Ensure client.Links is not nil before unmarshalling if it can be
		if client.Links != nil {
			err = json.Unmarshal(client.Links, &clientLinks)
			if err != nil {
				return common.NewErrorf("failed to unmarshal client.Links for client ID %d: %w", client.Id, err)
			}
		}

		newClientLinks := []map[string]string{}
		for _, inbound := range inbounds {
			newLinks := util.LinkGenerator(client.Config, &inbound, hostname)
			for _, newLink := range newLinks {
				newClientLinks = append(newClientLinks, map[string]string{
					"remark": inbound.Tag,
					"type":   "local",
					"uri":    newLink,
				})
			}
		}

		// Add no local links
		for _, clientLink := range clientLinks {
			if clientLink["type"] != "local" {
				newClientLinks = append(newClientLinks, clientLink)
			}
		}

		clients[index].Links, err = json.MarshalIndent(newClientLinks, "", "  ")
		if err != nil {
			return common.NewErrorf("failed to marshal new client links for client ID %d: %w", client.Id, err)
		}
	}
	return nil
}

func (s *ClientService) UpdateClientsOnInboundAdd(tx *gorm.DB, initIds string, inboundId uint, hostname string) error {
	clientIds := strings.Split(initIds, ",")
	if len(clientIds) == 0 || (len(clientIds) == 1 && clientIds[0] == "") {
		return nil // No client IDs provided
	}
	var clients []model.Client
	err := tx.Model(model.Client{}).Where("id in ?", clientIds).Find(&clients).Error
	if err != nil {
		return common.NewErrorf("failed to find clients for inbound add: %w", err)
	}
	var inbound model.Inbound
	err = tx.Model(model.Inbound{}).Preload("Tls").Where("id = ?", inboundId).Find(&inbound).Error
	if err != nil {
		return common.NewErrorf("failed to find inbound ID %d: %w", inboundId, err)
	}
	for _, client := range clients {
		// Add inbounds
		var clientInbounds []uint
		if client.Inbounds != nil { // Check if Inbounds is nil before unmarshalling
			err = json.Unmarshal(client.Inbounds, &clientInbounds)
			if err != nil {
				return common.NewErrorf("failed to unmarshal client.Inbounds for client ID %d: %w", client.Id, err)
			}
		}
		clientInbounds = append(clientInbounds, inboundId)
		client.Inbounds, err = json.MarshalIndent(clientInbounds, "", "  ")
		if err != nil {
			return common.NewErrorf("failed to marshal client.Inbounds for client ID %d: %w", client.Id, err)
		}
		// Add links
		var clientLinks, newClientLinks []map[string]string
		if client.Links != nil { // Check if Links is nil before unmarshalling
			err = json.Unmarshal(client.Links, &clientLinks)
			if err != nil {
				return common.NewErrorf("failed to unmarshal client.Links for client ID %d: %w", client.Id, err)
			}
		}
		newLinks := util.LinkGenerator(client.Config, &inbound, hostname)
		for _, newLink := range newLinks {
			newClientLinks = append(newClientLinks, map[string]string{
				"remark": inbound.Tag,
				"type":   "local",
				"uri":    newLink,
			})
		}
		for _, clientLink := range clientLinks {
			if clientLink["remark"] != inbound.Tag {
				newClientLinks = append(newClientLinks, clientLink)
			}
		}

		client.Links, err = json.MarshalIndent(newClientLinks, "", "  ")
		if err != nil {
			return common.NewErrorf("failed to marshal client.Links for client ID %d: %w", client.Id, err)
		}
		err = tx.Save(&client).Error
		if err != nil {
			return common.NewErrorf("failed to save client ID %d after inbound add: %w", client.Id, err)
		}
	}
	return nil
}

func (s *ClientService) UpdateClientsOnInboundDelete(tx *gorm.DB, id uint, tag string) error {
	var clients []model.Client
	err := tx.Table("clients").
		Where("EXISTS (SELECT 1 FROM json_each(clients.inbounds) WHERE json_each.value = ?)", id).
		Find(&clients).Error
	if err != nil {
		return common.NewErrorf("failed to find clients for inbound delete (inbound ID %d): %w", id, err)
	}
	for _, client := range clients {
		// Delete inbounds
		var clientInbounds, newClientInbounds []uint
		if client.Inbounds != nil {
			err = json.Unmarshal(client.Inbounds, &clientInbounds)
			if err != nil {
				return common.NewErrorf("failed to unmarshal client.Inbounds for client ID %d: %w", client.Id, err)
			}
		}
		for _, clientInbound := range clientInbounds {
			if clientInbound != id {
				newClientInbounds = append(newClientInbounds, clientInbound)
			}
		}
		client.Inbounds, err = json.MarshalIndent(newClientInbounds, "", "  ")
		if err != nil {
			return common.NewErrorf("failed to marshal client.Inbounds for client ID %d: %w", client.Id, err)
		}
		// Delete links
		var clientLinks, newClientLinks []map[string]string
		if client.Links != nil {
			err = json.Unmarshal(client.Links, &clientLinks)
			if err != nil {
				return common.NewErrorf("failed to unmarshal client.Links for client ID %d: %w", client.Id, err)
			}
		}
		for _, clientLink := range clientLinks {
			if clientLink["remark"] != tag {
				newClientLinks = append(newClientLinks, clientLink)
			}
		}
		client.Links, err = json.MarshalIndent(newClientLinks, "", "  ")
		if err != nil {
			return common.NewErrorf("failed to marshal client.Links for client ID %d: %w", client.Id, err)
		}
		err = tx.Save(&client).Error
		if err != nil {
			return common.NewErrorf("failed to save client ID %d after inbound delete: %w", client.Id, err)
		}
	}
	return nil
}

func (s *ClientService) UpdateLinksByInboundChange(tx *gorm.DB, inbounIds []uint, hostname string) error {
	var inbounds []model.Inbound
	err := tx.Model(model.Inbound{}).Preload("Tls").Where("id in ? and type in ?", inbounIds, util.InboundTypeWithLink).Find(&inbounds).Error
	if err != nil {
		if database.IsNotFound(err) {
			return nil // No matching inbounds found, not an error, just nothing to do.
		}
		return common.NewErrorf("failed to find inbounds for link change: %w", err)
	}
	for _, inbound := range inbounds {
		var clients []model.Client
		err = tx.Table("clients").
			Where("EXISTS (SELECT 1 FROM json_each(clients.inbounds) WHERE json_each.value = ?)", inbound.Id).
			Find(&clients).Error
		if err != nil {
			return common.NewErrorf("failed to find clients for inbound ID %d link change: %w", inbound.Id, err)
		}
		for _, client := range clients {
			var clientLinks, newClientLinks []map[string]string
			if client.Links != nil {
				err = json.Unmarshal(client.Links, &clientLinks)
				if err != nil {
					return common.NewErrorf("failed to unmarshal client.Links for client ID %d: %w", client.Id, err)
				}
			}
			newLinks := util.LinkGenerator(client.Config, &inbound, hostname)
			for _, newLink := range newLinks {
				newClientLinks = append(newClientLinks, map[string]string{
					"remark": inbound.Tag,
					"type":   "local",
					"uri":    newLink,
				})
			}
			for _, clientLink := range clientLinks {
				if clientLink["remark"] != inbound.Tag {
					newClientLinks = append(newClientLinks, clientLink)
				}
			}

			client.Links, err = json.MarshalIndent(newClientLinks, "", "  ")
			if err != nil {
				return common.NewErrorf("failed to marshal client.Links for client ID %d: %w", client.Id, err)
			}
			err = tx.Save(&client).Error
			if err != nil {
				return common.NewErrorf("failed to save client ID %d after link change: %w", client.Id, err)
			}
		}
	}
	return nil
}

func (s *ClientService) DepleteClients() error {
	var err error
	var clients []model.Client
	var changes []model.Changes
	var inboundIds []uint

	now := time.Now().Unix()
	db := database.GetDB()

	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
			if len(inboundIds) > 0 && corePtr.IsRunning() {
				// Pass tx to RestartInbounds to ensure atomicity
				err1 := s.InboundService.RestartInbounds(tx, inboundIds) // Changed db to tx
				if err1 != nil {
					logger.Error("unable to restart inbounds: ", err1)
				}
			}
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Model(model.Client{}).Where("enable = true AND ((volume >0 AND up+down > volume) OR (expiry > 0 AND expiry < ?))", now).Find(&clients).Error
	if err != nil {
		// Wrap GORM errors for better context if this function returns the error directly
		return common.NewErrorf("failed to find clients for depletion: %w", err)
	}

	dt := time.Now().Unix()
	for _, client := range clients {
		logger.Debug("Client ", client.Name, " is going to be disabled")
		var userInbounds []uint
		if client.Inbounds != nil {
			err = json.Unmarshal(client.Inbounds, &userInbounds)
			if err != nil {
				// Log the error and continue to disable other clients, or return the error.
				// For a batch job, logging and continuing might be preferable.
				logger.Errorf("failed to unmarshal client.Inbounds for client %s (ID %d) during depletion: %v", client.Name, client.Id, err)
				// Decide if this error should halt the entire depletion or just skip this client's inbounds for restart.
				// For now, we'll let it proceed to collect other inboundIds, but this client's inbounds might be missed for restart.
				// Clear the error so the transaction can continue for other clients
				err = nil
			}
		}
		inboundIds = s.uniqueAppendInboundIds(inboundIds, userInbounds)
		changes = append(changes, model.Changes{
			DateTime: dt,
			Actor:    "DepleteJob",
			Key:      "clients",
			Action:   "disable",
			Obj:      json.RawMessage("\"" + client.Name + "\""),
		})
	}

	// Save changes
	if len(changes) > 0 {
		err = tx.Model(model.Client{}).Where("enable = true AND ((volume >0 AND up+down > volume) OR (expiry > 0 AND expiry < ?))", now).Update("enable", false).Error
		if err != nil {
			return common.NewErrorf("failed to update clients to disabled state during depletion: %w", err)
		}
		err = tx.Model(model.Changes{}).Create(&changes).Error
		if err != nil {
			return common.NewErrorf("failed to create change log during client depletion: %w", err)
		}
		LastUpdate = dt
	}

	return nil
}

// avoid duplicate inboundIds
func (s *ClientService) uniqueAppendInboundIds(a []uint, b []uint) []uint {
	m := make(map[uint]bool)
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		m[v] = true
	}
	var res []uint
	for k := range m {
		res = append(res, k)
	}
	return res
}
