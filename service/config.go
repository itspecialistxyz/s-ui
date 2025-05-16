package service

import (
	"encoding/json"
	"s-ui/core"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/logger"
	"s-ui/util/common"
	"strconv"
	"time"
)

var (
	LastUpdate int64
	corePtr    *core.Core
)

type ConfigService struct {
	ClientService
	TlsService
	SettingService
	InboundService
	OutboundService
	EndpointService
}

type SingBoxConfig struct {
	Log          json.RawMessage   `json:"log"`
	Dns          json.RawMessage   `json:"dns"`
	Ntp          json.RawMessage   `json:"ntp"`
	Inbounds     []json.RawMessage `json:"inbounds"`
	Outbounds    []json.RawMessage `json:"outbounds"`
	Endpoints    []json.RawMessage `json:"endpoints"`
	Route        json.RawMessage   `json:"route"`
	Experimental json.RawMessage   `json:"experimental"`
}

func NewConfigService(core *core.Core) *ConfigService {
	corePtr = core
	return &ConfigService{}
}

func (s *ConfigService) GetConfig(data string) (*SingBoxConfig, error) {
	var err error
	if len(data) == 0 {
		data, err = s.SettingService.GetConfig()
		if err != nil {
			return nil, common.NewErrorf("failed to get base config from settings: %w", err)
		}
	}
	singboxConfig := SingBoxConfig{}
	err = json.Unmarshal([]byte(data), &singboxConfig)
	if err != nil {
		return nil, common.NewErrorf("failed to unmarshal base config: %w", err)
	}

	singboxConfig.Inbounds, err = s.InboundService.GetAllConfig(database.GetDB())
	if err != nil {
		return nil, common.NewErrorf("failed to get all inbound configs: %w", err)
	}
	singboxConfig.Outbounds, err = s.OutboundService.GetAllConfig(database.GetDB())
	if err != nil {
		return nil, common.NewErrorf("failed to get all outbound configs: %w", err)
	}
	singboxConfig.Endpoints, err = s.EndpointService.GetAllConfig(database.GetDB())
	if err != nil {
		return nil, common.NewErrorf("failed to get all endpoint configs: %w", err)
	}
	return &singboxConfig, nil
}

func (s *ConfigService) StartCore(defaultConfig string) error {
	if corePtr.IsRunning() {
		return nil
	}
	singboxConfig, err := s.GetConfig(defaultConfig)
	if err != nil {
		return common.NewErrorf("failed to get full config for core start: %w", err)
	}
	rawConfig, err := json.MarshalIndent(singboxConfig, "", "  ")
	if err != nil {
		return common.NewErrorf("failed to marshal full config for core start: %w", err)
	}
	err = corePtr.Start(rawConfig)
	if err != nil {
		// Log the original error before wrapping
		logger.Errorf("start sing-box err: %v", err)
		return common.NewErrorf("failed to start sing-box core: %w", err)
	}
	logger.Info("sing-box started")
	return nil
}

func (s *ConfigService) RestartCore() error {
	err := s.StopCore()
	if err != nil {
		return common.NewErrorf("failed to stop core during restart: %w", err)
	}
	return s.StartCore("")
}

func (s *ConfigService) restartCoreWithConfig(config json.RawMessage) error {
	err := s.StopCore()
	if err != nil {
		return common.NewErrorf("failed to stop core before restarting with new config: %w", err)
	}
	return s.StartCore(string(config))
}

func (s *ConfigService) StopCore() error {
	err := corePtr.Stop()
	if err != nil {
		return common.NewErrorf("failed to stop sing-box core: %w", err)
	}
	logger.Info("sing-box stopped")
	return nil
}

func (s *ConfigService) Save(obj string, act string, data json.RawMessage, initUsers string, loginUser string, hostname string) (objs []string, err error) { // Added named return for err
	var inboundIdsToRestart []uint // Renamed to avoid confusion with inboundId
	var singleInboundId uint       // Stores ID from inbound save operation
	objs = []string{obj}

	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			// Re-panic to propagate the panic upwards after rolling back
			// It's generally better to avoid panics and handle errors explicitly
			panic(p)
		} else if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit().Error // Capture commit error
			if err == nil {
				if len(inboundIdsToRestart) > 0 && corePtr.IsRunning() {
					// Use db for RestartInbounds as the transaction is committed.
					// If RestartInbounds needs to be part of the main transaction, this logic needs adjustment.
					errRestart := s.InboundService.RestartInbounds(db, inboundIdsToRestart)
					if errRestart != nil {
						logger.Errorf("unable to restart inbounds: %v", errRestart)
						// Decide if this error should be propagated. For now, logging it.
					}
				}
				// Try to start core if it is not running
				if !corePtr.IsRunning() {
					// Use "" for defaultConfig, assuming GetConfig will fetch the latest from DB
					errStart := s.StartCore("")
					if errStart != nil {
						logger.Errorf("failed to auto-start core after save: %v", errStart)
						// Decide if this error should be propagated.
					}
				}
				LastUpdate = time.Now().Unix()
			}
		}
	}()

	switch obj {
	case "clients":
		inboundIdsToRestart, err = s.ClientService.Save(tx, act, data, hostname)
		if err != nil {
			err = common.NewErrorf("failed to save clients: %w", err)
			return
		}
		objs = append(objs, "inbounds")
	case "tls":
		inboundIdsToRestart, err = s.TlsService.Save(tx, act, data)
		if err != nil {
			err = common.NewErrorf("failed to save tls: %w", err)
			return
		}
	case "inbounds":
		var actualInboundIdToRestart uint // To be populated by InboundService.Save
		var tagForClientUpdate string

		if act == "del" {
			var idToDelete uint
			// For deletion, 'data' is expected to be the ID of the inbound (as a JSON number).
			if errUnmarshal := json.Unmarshal(data, &idToDelete); errUnmarshal != nil {
				err = common.NewErrorf("failed to unmarshal inbound ID for deletion from 'data': %w", errUnmarshal)
				return // Triggers rollback in defer
			}
			if idToDelete == 0 {
				err = common.NewError("inbound ID for deletion cannot be zero")
				return // Triggers rollback
			}
			var tempInbound model.Inbound
			// Fetch the tag using the ID from 'data' before it's conceptually deleted by InboundService.Save
			if errDb := tx.Model(&model.Inbound{}).Where("id = ?", idToDelete).Select("tag").First(&tempInbound).Error; errDb != nil {
				err = common.NewErrorf("failed to retrieve tag for inbound ID %d prior to deletion: %w", idToDelete, errDb)
				return // Triggers rollback
			}
			tagForClientUpdate = tempInbound.Tag
			// 'data' (containing the ID) is passed as is to InboundService.Save
		}

		// Call InboundService.Save. For "del", it uses the ID in 'data' and returns it.
		actualInboundIdToRestart, err = s.InboundService.Save(tx, act, data, initUsers, hostname)
		if err != nil {
			// This error will be wrapped by the main error handling for "inbounds" case below
			return
		}

		// If an inbound was added or edited, its ID should be considered for restart
		if act == "new" || act == "edit" {
			inboundIdsToRestart = append(inboundIdsToRestart, actualInboundIdToRestart)
		} else if act == "del" {
			// For delete, actualInboundIdToRestart is the ID of the deleted inbound.
			inboundIdsToRestart = append(inboundIdsToRestart, actualInboundIdToRestart)
		}

		// Client updates based on action
		idsForClientLinkUpdate := []uint{}
		if actualInboundIdToRestart != 0 {
			idsForClientLinkUpdate = append(idsForClientLinkUpdate, actualInboundIdToRestart)
		}

		switch act {
		case "new":
			err = s.ClientService.UpdateClientsOnInboundAdd(tx, initUsers, actualInboundIdToRestart, hostname)
		case "edit":
			if len(idsForClientLinkUpdate) > 0 {
				err = s.ClientService.UpdateLinksByInboundChange(tx, idsForClientLinkUpdate, hostname)
			}
		case "del":
			// actualInboundIdToRestart is the ID of the deleted inbound.
			// tagForClientUpdate was fetched before InboundService.Save was called.
			if tagForClientUpdate == "" && actualInboundIdToRestart != 0 { // Check if tag is empty for a valid deleted ID
				logger.Warningf("Attempting to update clients for deleted inbound ID %d with an empty tag. This might be intended or indicate an issue if the tag was expected.", actualInboundIdToRestart)
			}
			err = s.ClientService.UpdateClientsOnInboundDelete(tx, actualInboundIdToRestart, tagForClientUpdate)
		}
		// Error from ClientService calls will be caught by the main error handling for "inbounds" case
		if err != nil {
			return
		}
		objs = append(objs, "clients")
	case "outbounds":
		err = s.OutboundService.Save(tx, act, data)
		if err != nil {
			err = common.NewErrorf("failed to save outbounds: %w", err)
			return
		}
	case "endpoints":
		err = s.EndpointService.Save(tx, act, data)
		if err != nil {
			err = common.NewErrorf("failed to save endpoints: %w", err)
			return
		}
	case "config":
		// The 'data' here is the JSON string for the core config.
		// The SettingService.Update method handles saving this to the "config" key.
		err = s.SettingService.Update(tx, "config", string(data))
		if err != nil {
			err = common.NewErrorf("failed to save config using SettingService.Update: %w", err)
			return
		}
		// Restart core with the new config.
		err = s.restartCoreWithConfig(data)
		if err != nil {
			err = common.NewErrorf("failed to restart core with new config: %w", err)
			return // This will trigger rollback in defer
		}

	case "settings":
		// 'data' for "settings" is expected to be a JSON object like {"key1":"value1", "key2":"value2"}
		var settingsToUpdate map[string]string
		if errUnmarshal := json.Unmarshal(data, &settingsToUpdate); errUnmarshal != nil {
			err = common.NewErrorf("failed to unmarshal settings data: %w", errUnmarshal)
			return
		}
		for key, value := range settingsToUpdate {
			err = s.SettingService.Update(tx, key, value)
			if err != nil {
				err = common.NewErrorf("failed to save setting '%s': %w", key, err)
				return // Rollback on first error
			}
		}
	default:
		err = common.NewErrorf("unknown object type: %s", obj)
		return
	}
	// If any of the above cases returned an error, 'err' is set and defer will rollback.

	dt := time.Now().Unix()
	changeLog := model.Changes{
		DateTime: dt,
		Actor:    loginUser,
		Key:      obj,
		Action:   act,
		Obj:      data, // Consider if storing raw data is always appropriate or if a summary/ID is better
	}
	err = tx.Create(&changeLog).Error
	if err != nil {
		err = common.NewErrorf("failed to create change log: %w", err)
		return
	}

	// Update side changes (still within the same transaction)
	if obj == "tls" && len(inboundIdsToRestart) > 0 { // use inboundIdsToRestart
		err = s.ClientService.UpdateLinksByInboundChange(tx, inboundIdsToRestart, hostname)
		if err != nil {
			err = common.NewErrorf("failed to update client links after tls change: %w", err)
			return
		}
		objs = append(objs, "clients")

		err = s.InboundService.UpdateOutJsons(tx, inboundIdsToRestart, hostname)
		if err != nil {
			// Consistent error wrapping
			err = common.NewErrorf("unable to update out_json of inbounds after tls change: %w", err)
			return
		}
		objs = append(objs, "inbounds")
	}

	if obj == "inbounds" {
		// singleInboundId holds the ID of the inbound that was new/edited/deleted
		idsForClientUpdate := []uint{}
		if singleInboundId != 0 { // Ensure singleInboundId is valid
			idsForClientUpdate = append(idsForClientUpdate, singleInboundId)
		}

		switch act {
		case "new":
			// initUsers might be a comma-separated string of client IDs
			err = s.ClientService.UpdateClientsOnInboundAdd(tx, initUsers, singleInboundId, hostname)
		case "edit":
			if len(idsForClientUpdate) > 0 {
				err = s.ClientService.UpdateLinksByInboundChange(tx, idsForClientUpdate, hostname)
			}
		case "del":
			var tag string
			// The 'data' for 'del' inbound is just the tag string, not a full JSON object.
			// It's better to pass the tag directly if possible, or unmarshal carefully.
			// Assuming 'data' is a JSON string like "\"the-tag\""
			if unmarshalErr := json.Unmarshal(data, &tag); unmarshalErr != nil {
				err = common.NewErrorf("failed to unmarshal tag for inbound deletion: %w", unmarshalErr)
				return
			}
			// singleInboundId here is the ID of the inbound that was deleted.
			err = s.ClientService.UpdateClientsOnInboundDelete(tx, singleInboundId, tag)
		}
		if err != nil {
			// Wrap specific errors from client service updates
			err = common.NewErrorf("failed to update clients after inbound %s: %w", act, err)
			return
		}
		objs = append(objs, "clients")
	}
	// err is nil here, so defer will commit.
	return
}

func (s *ConfigService) CheckChanges(lu string) (bool, error) {
	if lu == "" {
		return true, nil // No last update timestamp provided, assume changes exist
	}

	lastUpdateUnix, err := strconv.ParseInt(lu, 10, 64)
	if err != nil {
		return false, common.NewErrorf("invalid last update timestamp format '%s': %w", lu, err)
	}

	// If LastUpdate (in-memory cache) is more recent, then there are changes.
	if LastUpdate > lastUpdateUnix {
		return true, nil
	}

	// If in-memory cache is not more recent, query DB.
	// This handles the case where the service might have restarted and LastUpdate is 0 or old.
	db := database.GetDB()
	var count int64
	// Use parameterized query to prevent SQL injection
	queryErr := db.Model(&model.Changes{}).Where("date_time > ?", lastUpdateUnix).Count(&count).Error
	if queryErr != nil {
		return false, common.NewErrorf("failed to query changes from database: %w", queryErr)
	}

	if count > 0 {
		// Update in-memory LastUpdate if DB shows more recent changes than 'lu'
		// and potentially more recent than current LastUpdate (though this specific query doesn't give the latest time)
		// A simple approach is to set it to now, assuming if we checked, we are "up-to-date" with this check.
		// A more accurate LastUpdate would require fetching the max(date_time) from changes.
		LastUpdate = time.Now().Unix()
		return true, nil
	}

	return false, nil
}

func (s *ConfigService) GetChanges(actor string, chngKey string, countStr string) ([]model.Changes, error) {
	c, err := strconv.Atoi(countStr)
	if err != nil {
		return nil, common.NewErrorf("invalid count parameter '%s': %w", countStr, err)
	}
	if c <= 0 {
		// Or handle as "no limit" if that's desired, but typically a positive count is expected.
		return []model.Changes{}, nil
	}

	db := database.GetDB()
	query := db.Model(&model.Changes{})

	if len(actor) > 0 {
		query = query.Where("actor = ?", actor)
	}
	if len(chngKey) > 0 {
		query = query.Where("`key` = ?", chngKey) // Ensure 'key' is quoted if it's a reserved keyword
	}

	var chngs []model.Changes
	// Use Find instead of Scan for loading into a slice of structs
	dbErr := query.Order("id desc").Limit(c).Find(&chngs).Error
	if dbErr != nil {
		// Log the error, but also return it so the caller can handle it.
		logger.Warningf("failed to get changes: %v", dbErr)
		return nil, common.NewErrorf("failed to retrieve changes: %w", dbErr)
	}
	return chngs, nil
}
