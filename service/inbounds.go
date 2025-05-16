package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/util"
	"s-ui/util/common"
	"strings"

	"gorm.io/gorm"
)

type InboundService struct{}

func (s *InboundService) Get(ids string) (*[]map[string]interface{}, error) {
	if ids == "" {
		return s.GetAll()
	}
	return s.getById(ids)
}

func (s *InboundService) getById(ids string) (*[]map[string]interface{}, error) {
	var inbound []model.Inbound
	var result []map[string]interface{}
	db := database.GetDB()
	err := db.Model(model.Inbound{}).Where("id in ?", strings.Split(ids, ",")).Scan(&inbound).Error
	if err != nil {
		return nil, err
	}
	for _, inb := range inbound {
		inbData, err := inb.MarshalFull()
		if err != nil {
			return nil, err
		}
		result = append(result, *inbData)
	}
	return &result, nil
}

func (s *InboundService) GetAll() (*[]map[string]interface{}, error) {
	db := database.GetDB()
	inbounds := []model.Inbound{}
	err := db.Model(model.Inbound{}).Scan(&inbounds).Error
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	for _, inbound := range inbounds {
		var shadowtls_version uint
		inbData := map[string]interface{}{
			"id":     inbound.Id,
			"type":   inbound.Type,
			"tag":    inbound.Tag,
			"tls_id": inbound.TlsId,
		}
		if inbound.Options != nil {
			var restFields map[string]json.RawMessage
			if err := json.Unmarshal(inbound.Options, &restFields); err != nil {
				log.Printf("Warning: Failed to unmarshal options for inbound ID %d (Tag: %s): %v. Skipping options for this inbound.", inbound.Id, inbound.Tag, err)
			} else {
				if listen, ok := restFields["listen"]; ok {
					inbData["listen"] = listen
				}
				if listenPort, ok := restFields["listen_port"]; ok {
					inbData["listen_port"] = listenPort
				}
				if inbound.Type == "shadowtls" {
					if versionRaw, ok := restFields["version"]; ok {
						if err := json.Unmarshal(versionRaw, &shadowtls_version); err != nil {
							log.Printf("Warning: Failed to unmarshal shadowtls version for inbound ID %d (Tag: %s): %v. Assuming version 0.", inbound.Id, inbound.Tag, err)
							shadowtls_version = 0 // Explicitly set to 0 on error
						}
					} else {
						log.Printf("Warning: Missing shadowtls version for inbound ID %d (Tag: %s). Assuming version 0.", inbound.Id, inbound.Tag)
						shadowtls_version = 0 // Explicitly set to 0 if key is missing
					}
				}
			}
		}
		if s.hasUser(inbound.Type) {
			if inbound.Type == "shadowtls" && shadowtls_version < 3 {
				log.Printf("Info: Skipping user fetching for shadowtls inbound ID %d (Tag: %s) due to version %d < 3.", inbound.Id, inbound.Tag, shadowtls_version)
				// Removed 'break', using 'continue' if we want to skip this item and process others
				// Or, if this item should still be added without users, this block can be just for logging.
				// For now, let's assume we still add the inbound but without users if this condition is met.
			} else {
				users := []string{}
				err = db.Raw("SELECT clients.name FROM clients, json_each(clients.inbounds) as je WHERE je.value = ?", inbound.Id).Scan(&users).Error
				if err != nil {
					// Decide on error handling: return error, or log and continue without users for this inbound
					log.Printf("Warning: Failed to fetch users for inbound ID %d (Tag: %s): %v", inbound.Id, inbound.Tag, err)
					// data = append(data, inbData) // Optionally add inbound even if user fetching fails
					// continue
					return nil, fmt.Errorf("failed to fetch users for inbound ID %d (Tag: %s): %w", inbound.Id, inbound.Tag, err) // Current: fail hard
				}
				inbData["users"] = users
			}
		}

		data = append(data, inbData)
	}
	return &data, nil
}

func (s *InboundService) FromIds(ids []uint) ([]*model.Inbound, error) {
	db := database.GetDB()
	inbounds := []*model.Inbound{}
	err := db.Model(model.Inbound{}).Where("id in ?", ids).Scan(&inbounds).Error
	if err != nil {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) Save(tx *gorm.DB, act string, data json.RawMessage, initUserIds string, hostname string) (uint, error) {
	var err error
	var id uint

	switch act {
	case "new", "edit":
		var inbound model.Inbound
		err = inbound.UnmarshalJSON(data)
		if err != nil {
			return 0, err
		}
		if inbound.TlsId > 0 {
			err = tx.Model(model.Tls{}).Where("id = ?", inbound.TlsId).Find(&inbound.Tls).Error
			if err != nil {
				return 0, err
			}
		}

		err = util.FillOutJson(&inbound, hostname)
		if err != nil {
			return 0, err
		}

		err = tx.Save(&inbound).Error
		if err != nil {
			return 0, err
		}
		id = inbound.Id

		if corePtr.IsRunning() {
			if act == "edit" {
				var oldTag string
				err = tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Pluck("tag", &oldTag).Error
				if err != nil {
					if err == gorm.ErrRecordNotFound {
						log.Printf("Warning: Inbound ID %d not found when trying to retrieve old tag for edit. Proceeding as if it's a new entry for core operations.", inbound.Id)
						oldTag = "" // Treat as if no old tag existed
					} else {
						return 0, fmt.Errorf("failed to retrieve old tag for inbound ID %d: %w", inbound.Id, err)
					}
				}
				if oldTag != "" { // Only attempt removal if oldTag was found and is not empty
					err = corePtr.RemoveInbound(oldTag)
					if err != nil && err != os.ErrInvalid { // os.ErrInvalid might mean tag not found in core, which is fine
						return 0, fmt.Errorf("failed to remove old inbound '%s' from core: %w", oldTag, err)
					}
				}
			}

			inboundConfig, err := inbound.MarshalJSON()
			if err != nil {
				return 0, err
			}

			if act == "edit" {
				inboundConfig, err = s.addUsers(tx, inboundConfig, inbound.Id, inbound.Type)
			} else {
				inboundConfig, err = s.initUsers(tx, inboundConfig, initUserIds, inbound.Type)
			}
			if err != nil {
				return 0, err
			}

			err = corePtr.AddInbound(inboundConfig)
			if err != nil {
				return 0, err
			}
		}
	case "del":
		var tag string
		err = json.Unmarshal(data, &tag)
		if err != nil {
			return 0, err
		}
		if corePtr.IsRunning() {
			err = corePtr.RemoveInbound(tag)
			if err != nil && err != os.ErrInvalid {
				return 0, err
			}
		}
		err = tx.Model(model.Inbound{}).Select("id").Where("tag = ?", tag).Scan(&id).Error
		if err != nil {
			return 0, err
		}
		err = tx.Where("tag = ?", tag).Delete(model.Inbound{}).Error
		if err != nil {
			return 0, err
		}
	default:
		return 0, common.NewErrorf("unknown action: %s", act)
	}
	return id, nil
}

func (s *InboundService) UpdateOutJsons(tx *gorm.DB, inboundIds []uint, hostname string) error {
	var inbounds []model.Inbound
	err := tx.Model(model.Inbound{}).Preload("Tls").Where("id in ?", inboundIds).Find(&inbounds).Error
	if err != nil {
		return err
	}
	for _, inbound := range inbounds {
		err = util.FillOutJson(&inbound, hostname)
		if err != nil {
			return err
		}
		err = tx.Model(model.Inbound{}).Where("tag = ?", inbound.Tag).Update("out_json", inbound.OutJson).Error
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *InboundService) GetAllConfig(db *gorm.DB) ([]json.RawMessage, error) {
	var inboundsJson []json.RawMessage
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Preload("Tls").Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		inboundJson, err := inbound.MarshalJSON()
		if err != nil {
			return nil, err
		}
		inboundJson, err = s.addUsers(db, inboundJson, inbound.Id, inbound.Type)
		if err != nil {
			return nil, err
		}
		inboundsJson = append(inboundsJson, inboundJson)
	}
	return inboundsJson, nil
}

func (s *InboundService) hasUser(inboundType string) bool {
	switch inboundType {
	case "mixed", "socks", "http", "shadowsocks", "vmess", "trojan", "naive", "hysteria", "shadowtls", "tuic", "hysteria2", "vless":
		return true
	}
	return false
}

func (s *InboundService) fetchUsers(db *gorm.DB, inboundType string, condition string, inbound map[string]interface{}) ([]json.RawMessage, error) {
	if inboundType == "shadowtls" {
		if versionRaw, ok := inbound["version"]; ok {
			var versionFloat float64
			if err := json.Unmarshal(versionRaw.(json.RawMessage), &versionFloat); err != nil {
				log.Printf("Warning: could not unmarshal shadowtls version from inbound options: %v. Assuming version 0.", err)
				versionFloat = 0
			}
			if int(versionFloat) < 3 {
				return nil, nil
			}
		} else {
			log.Printf("Warning: shadowtls version not found in inbound options. Assuming version 0 for user fetching logic.")
			return nil, nil // Or handle as version 0, which means no users for < 3
		}
	}
	if inboundType == "shadowsocks" {
		if methodRaw, ok := inbound["method"]; ok {
			var method string
			if err := json.Unmarshal(methodRaw.(json.RawMessage), &method); err != nil {
				log.Printf("Warning: could not unmarshal shadowsocks method from inbound options: %v", err)
			} else {
				if method == "2022-blake3-aes-128-gcm" {
					inboundType = "shadowsocks16"
				}
			}
		} else {
			log.Printf("Warning: shadowsocks method not found in inbound options.")
		}
	}

	var users []string
	err := db.Raw(`SELECT json_extract(clients.config, ?) FROM clients WHERE enable = true AND ?`,
		"$."+inboundType, condition).Scan(&users).Error
	if err != nil {
		return nil, err
	}
	var usersJson []json.RawMessage
	for _, user := range users {
		if inboundType == "vless" && inbound["tls"] == nil {
			user = strings.Replace(user, "xtls-rprx-vision", "", -1)
		}
		usersJson = append(usersJson, json.RawMessage(user))
	}
	return usersJson, nil
}

func (s *InboundService) addUsers(db *gorm.DB, inboundJson []byte, inboundId uint, inboundType string) ([]byte, error) {
	if !s.hasUser(inboundType) {
		return inboundJson, nil
	}

	var inbound map[string]interface{}
	err := json.Unmarshal(inboundJson, &inbound)
	if err != nil {
		return nil, err
	}

	condition := fmt.Sprintf("%d IN (SELECT json_each.value FROM json_each(clients.inbounds))", inboundId)
	inbound["users"], err = s.fetchUsers(db, inboundType, condition, inbound)
	if err != nil {
		return nil, err
	}

	return json.Marshal(inbound)
}

func (s *InboundService) initUsers(db *gorm.DB, inboundJson []byte, clientIds string, inboundType string) ([]byte, error) {
	ClientIds := strings.Split(clientIds, ",")
	if len(ClientIds) == 0 {
		return inboundJson, nil
	}

	if !s.hasUser(inboundType) {
		return inboundJson, nil
	}

	var inbound map[string]interface{}
	err := json.Unmarshal(inboundJson, &inbound)
	if err != nil {
		return nil, err
	}

	condition := fmt.Sprintf("id IN (%s)", strings.Join(ClientIds, ","))
	inbound["users"], err = s.fetchUsers(db, inboundType, condition, inbound)
	if err != nil {
		return nil, err
	}

	return json.Marshal(inbound)
}

func (s *InboundService) RestartInbounds(tx *gorm.DB, ids []uint) error {
	var inbounds []*model.Inbound
	err := tx.Model(model.Inbound{}).Preload("Tls").Where("id in ?", ids).Find(&inbounds).Error
	if err != nil {
		return err
	}
	for _, inbound := range inbounds {
		err = corePtr.RemoveInbound(inbound.Tag)
		if err != nil && err != os.ErrInvalid {
			return err
		}
		inboundConfig, err := inbound.MarshalJSON()
		if err != nil {
			return err
		}
		inboundConfig, err = s.addUsers(tx, inboundConfig, inbound.Id, inbound.Type)
		if err != nil {
			return err
		}
		err = corePtr.AddInbound(inboundConfig)
		if err != nil {
			return err
		}
	}
	return nil
}
