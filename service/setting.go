package service

import (
	"os"
	"s-ui/config"
	"s-ui/database"
	"s-ui/database/model"
	"s-ui/logger"
	"s-ui/util/common"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

var defaultConfig = `{
  "log": {
    "level": "info"
  },
  "dns": {},
  "route": {
    "rules": [
      {
        "protocol": [
          "dns"
        ],
        "action": "hijack-dns"
      }
    ]
  },
  "experimental": {}
}`

var defaultValueMap = map[string]string{
	"webListen":     "",
	"webDomain":     "",
	"webPort":       "2095",
	"secret":        common.Random(32),
	"webCertFile":   "",
	"webKeyFile":    "",
	"webPath":       "/app/",
	"webURI":        "",
	"sessionMaxAge": "0",
	"trafficAge":    "30",
	"timeLocation":  "Asia/Tehran",
	"subListen":     "",
	"subPort":       "2096",
	"subPath":       "/sub/",
	"subDomain":     "",
	"subCertFile":   "",
	"subKeyFile":    "",
	"subUpdates":    "12",
	"subEncode":     "true",
	"subShowInfo":   "false",
	"subURI":        "",
	"subJsonExt":    "",
	"config":        defaultConfig,
	"version":       config.GetVersion(),
	"panelLanguage": "en",    // Added default
	"panelTheme":    "light", // Added default
}

type SettingService struct {
}

func (s *SettingService) GetAllSetting() (*map[string]string, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := map[string]string{}

	for _, setting := range settings {
		allSetting[setting.Key] = setting.Value
	}

	for key, defaultValue := range defaultValueMap {
		if _, exists := allSetting[key]; !exists {
			// Pass the db instance to saveSetting
			err = s.saveSetting(db, key, defaultValue)
			if err != nil {
				return nil, err
			}
			allSetting[key] = defaultValue
		}
	}

	// Due to security principles
	delete(allSetting, "secret")
	delete(allSetting, "config")
	delete(allSetting, "version")

	return &allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	return db.Where("1 = 1").Delete(model.Setting{}).Error
}

func (s *SettingService) getSetting(db *gorm.DB, key string) (*model.Setting, error) {
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) getString(db *gorm.DB, key string) (string, error) {
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if database.IsNotFound(err) {
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap and not found in DB", key)
		}
		// Optionally save the default value if it's missing from DB upon first request
		// logger.Infof("Key <%s> not found in DB, using default and saving.", key)
		// if errSave := s.saveSetting(db, key, value); errSave != nil {
		//  logger.Warningf("Failed to save default value for key <%s>: %v", key, errSave)
		// }
		return value, nil
	} else if err != nil {
		return "", common.NewErrorf("failed to get setting '%s': %w", key, err)
	}
	return setting.Value, nil
}

// saveSetting saves a key-value pair. It uses the provided db instance (which can be a transaction).
func (s *SettingService) saveSetting(db *gorm.DB, key string, value string) error {
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if database.IsNotFound(err) {
		return db.Create(&model.Setting{
			Key:   key,
			Value: value,
		}).Error
	} else if err != nil {
		return common.NewErrorf("failed to get setting '%s' for save: %w", key, err)
	}
	setting.Value = value // Update existing setting's value
	return db.Save(setting).Error
}

// Update updates a setting by key and value
// It uses a transaction if tx is not nil.
func (s *SettingService) Update(tx *gorm.DB, key string, value string) error {
	var err error
	var typedValue interface{} = value // Store the appropriately typed value

	switch key {
	case "webListen":
		typedValue = value
	case "webDomain":
		typedValue = value
	case "webPort":
		i, errConv := strconv.Atoi(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse webPort to int: %w", errConv)
		}
		typedValue = i
	case "webCertFile":
		typedValue = value
	case "webKeyFile":
		typedValue = value
	case "webPath":
		// Ensure path format consistency
		newPath := value
		if !strings.HasPrefix(newPath, "/") {
			newPath = "/" + newPath
		}
		if !strings.HasSuffix(newPath, "/") {
			newPath += "/"
		}
		typedValue = newPath
	case "webURI":
		typedValue = value
	case "secret":
		// Secrets should ideally be handled with more care, e.g. not directly updatable this way
		// or requiring re-encryption if stored encrypted.
		// For now, treating as a direct string update.
		typedValue = value
	case "sessionMaxAge":
		i, errConv := strconv.Atoi(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse sessionMaxAge to int: %w", errConv)
		}
		typedValue = i
	case "trafficAge":
		i, errConv := strconv.Atoi(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse trafficAge to int: %w", errConv)
		}
		typedValue = i
	case "timeLocation":
		// Validate if it's a valid time location
		_, errConv := time.LoadLocation(value)
		if errConv != nil {
			return common.NewErrorf("invalid time location '%s': %w", value, errConv)
		}
		typedValue = value
	case "subListen":
		typedValue = value
	case "subPort":
		i, errConv := strconv.Atoi(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse subPort to int: %w", errConv)
		}
		typedValue = i
	case "subPath":
		newPath := value
		if !strings.HasPrefix(newPath, "/") {
			newPath = "/" + newPath
		}
		if !strings.HasSuffix(newPath, "/") {
			newPath += "/"
		}
		typedValue = newPath
	case "subDomain":
		typedValue = value
	case "subCertFile":
		typedValue = value
	case "subKeyFile":
		typedValue = value
	case "subUpdates":
		i, errConv := strconv.Atoi(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse subUpdates to int: %w", errConv)
		}
		typedValue = i
	case "subEncode":
		b, errConv := strconv.ParseBool(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse subEncode to bool: %w", errConv)
		}
		typedValue = b
	case "subShowInfo":
		b, errConv := strconv.ParseBool(value)
		if errConv != nil {
			return common.NewErrorf("failed to parse subShowInfo to bool: %w", errConv)
		}
		typedValue = b
	case "subURI":
		typedValue = value
	case "subJsonExt":
		typedValue = value
	// Note: "config" and "version" are typically not updated via this generic method.
	// "config" (CoreConfig) is complex JSON and should have its own update mechanism if mutable.
	// "version" is derived from the application.
	default:
		// Check if it's a core setting - these are generally direct string assignments
		// or require specific handling if they are not simple strings.
		// For now, we assume if it's not in the explicit cases, it might be a direct string setting.
		// However, it's safer to return an error for unknown keys.
		if _, exists := defaultValueMap[key]; !exists {
			return common.NewErrorf("unknown setting key: %s", key)
		}
		// If it exists in defaultValueMap but not handled above, assume string
		typedValue = value
	}

	// Convert typedValue back to string for saveSetting, as it expects a string value.
	// saveSetting will handle creating/updating the key-value pair in the DB.
	var valueStr string
	switch v := typedValue.(type) {
	case string:
		valueStr = v
	case int:
		valueStr = strconv.Itoa(v)
	case bool:
		valueStr = strconv.FormatBool(v)
	default:
		// This should not happen if all cases are handled
		return common.NewErrorf("internal error: unhandled type for setting key %s", key)
	}

	dbToUse := database.GetDB()
	if tx != nil {
		dbToUse = tx
	}

	err = s.saveSetting(dbToUse, key, valueStr) // Pass dbToUse (which could be tx)
	if err != nil {
		return common.NewErrorf("Update: failed to save setting for key '%s': %w", key, err)
	}
	return nil
}

// Overwrite existing Getters to use the new getString, getInt, getBool which accept a DB instance.
// They will now fetch their own DB instance. If transactional behavior is needed for a sequence of gets,
// the calling code would need to manage the transaction and pass the *gorm.DB instance.

func (s *SettingService) GetListen() (string, error) {
	return s.getString(database.GetDB(), "webListen")
}

func (s *SettingService) GetWebDomain() (string, error) {
	return s.getString(database.GetDB(), "webDomain")
}

func (s *SettingService) GetPort() (int, error) {
	str, err := s.getString(database.GetDB(), "webPort")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

// SetPort now uses the generic Update method.
func (s *SettingService) SetPort(port int) error {
	return s.Update(nil, "webPort", strconv.Itoa(port)) // Pass nil for tx to use default DB handling
}

func (s *SettingService) GetCertFile() (string, error) {
	return s.getString(database.GetDB(), "webCertFile")
}

func (s *SettingService) GetKeyFile() (string, error) {
	return s.getString(database.GetDB(), "webKeyFile")
}

func (s *SettingService) GetWebPath() (string, error) {
	webPath, err := s.getString(database.GetDB(), "webPath")
	if err != nil {
		return "", err
	}
	// Path formatting is now handled in Update, but good to ensure consistency on read too.
	if webPath != "" { // only format if not empty
		if !strings.HasPrefix(webPath, "/") {
			webPath = "/" + webPath
		}
		if !strings.HasSuffix(webPath, "/") {
			webPath += "/"
		}
	}
	return webPath, nil
}

// SetWebPath now uses the generic Update method.
func (s *SettingService) SetWebPath(webPath string) error {
	return s.Update(nil, "webPath", webPath)
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString(database.GetDB(), "secret")
	// The logic for saving default secret if it matches defaultValueMap seems specific
	// and might be better handled during initialization or a dedicated "check and init" step.
	// For now, just return the value.
	return []byte(secret), err
}

func (s *SettingService) GetSessionMaxAge() (int, error) {
	str, err := s.getString(database.GetDB(), "sessionMaxAge")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) GetTrafficAge() (int, error) {
	str, err := s.getString(database.GetDB(), "trafficAge")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString(database.GetDB(), "timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocationStr := defaultValueMap["timeLocation"]
		logger.Errorf("Location '%v' not valid, using default location '%s': %v", l, defaultLocationStr, err)
		return time.LoadLocation(defaultLocationStr) // Attempt to load default
	}
	return location, nil
}

func (s *SettingService) GetSubListen() (string, error) {
	return s.getString(database.GetDB(), "subListen")
}

func (s *SettingService) GetSubPort() (int, error) {
	str, err := s.getString(database.GetDB(), "subPort")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) SetSubPort(subPort int) error {
	return s.Update(nil, "subPort", strconv.Itoa(subPort))
}

func (s *SettingService) GetSubPath() (string, error) {
	subPath, err := s.getString(database.GetDB(), "subPath")
	if err != nil {
		return "", err
	}
	if subPath != "" {
		if !strings.HasPrefix(subPath, "/") {
			subPath = "/" + subPath
		}
		if !strings.HasSuffix(subPath, "/") {
			subPath += "/"
		}
	}
	return subPath, nil
}

func (s *SettingService) SetSubPath(subPath string) error {
	return s.Update(nil, "subPath", subPath)
}

func (s *SettingService) GetSubDomain() (string, error) {
	return s.getString(database.GetDB(), "subDomain")
}

func (s *SettingService) GetSubCertFile() (string, error) {
	return s.getString(database.GetDB(), "subCertFile")
}

func (s *SettingService) GetSubKeyFile() (string, error) {
	return s.getString(database.GetDB(), "subKeyFile")
}

func (s *SettingService) GetSubUpdates() (int, error) {
	str, err := s.getString(database.GetDB(), "subUpdates")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) GetSubEncode() (bool, error) {
	str, err := s.getString(database.GetDB(), "subEncode")
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) GetSubShowInfo() (bool, error) {
	str, err := s.getString(database.GetDB(), "subShowInfo")
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) GetSubURI() (string, error) {
	return s.getString(database.GetDB(), "subURI")
}

func (s *SettingService) GetConfig() (string, error) {
	// This refers to the Core Config JSON string
	return s.getString(database.GetDB(), "config")
}

func (s *SettingService) SetConfig(configStr string) error {
	// This should be used carefully, as configStr is expected to be valid JSON for the core.
	return s.Update(nil, "config", configStr)
}

// GetWebSettings collects all web-related settings into a map.
func (s *SettingService) GetWebSettings() (map[string]interface{}, error) {
	db := database.GetDB() // Use a single DB instance for all gets
	settings := make(map[string]interface{})
	var err error
	var strVal string
	var intVal int
	// var boolVal bool // If any web settings are boolean

	strVal, err = s.getString(db, "webListen")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webListen: %w", err)
	}
	settings["webListen"] = strVal

	strVal, err = s.getString(db, "webDomain")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webDomain: %w", err)
	}
	settings["webDomain"] = strVal

	strVal, err = s.getString(db, "webPort")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webPort: %w", err)
	}
	intVal, err = strconv.Atoi(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to parse webPort: %w", err)
	}
	settings["webPort"] = intVal

	strVal, err = s.getString(db, "webCertFile")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webCertFile: %w", err)
	}
	settings["webCertFile"] = strVal

	strVal, err = s.getString(db, "webKeyFile")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webKeyFile: %w", err)
	}
	settings["webKeyFile"] = strVal

	webPath, err := s.GetWebPath() // Uses its own DB get, but applies formatting
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webPath: %w", err)
	}
	settings["webPath"] = webPath

	strVal, err = s.getString(db, "webURI")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get webURI: %w", err)
	}
	settings["webURI"] = strVal

	// "secret" is sensitive, usually not included in general "get all settings" type views.
	// If needed, it should be fetched explicitly.
	// secretBytes, err := s.GetSecret()
	// if err != nil { return nil, common.NewErrorf("GetWebSettings: failed to get secret: %w", err) }
	// settings["secret"] = string(secretBytes) // Or however it should be represented

	strVal, err = s.getString(db, "sessionMaxAge")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get sessionMaxAge: %w", err)
	}
	intVal, err = strconv.Atoi(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to parse sessionMaxAge: %w", err)
	}
	settings["sessionMaxAge"] = intVal

	// These are more general panel settings but were in the original GetWebSettings
	strVal, err = s.getString(db, "trafficAge")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get trafficAge: %w", err)
	}
	intVal, err = strconv.Atoi(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to parse trafficAge: %w", err)
	}
	settings["trafficAge"] = intVal

	timeLoc, err := s.GetTimeLocation() // Uses its own DB get
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get timeLocation: %w", err)
	}
	settings["timeLocation"] = timeLoc.String() // Store as string

	// Panel specific settings from original GetWebSettings
	strVal, err = s.getString(db, "panelLanguage")
	if err != nil {
		// If getString errors, it means it's not in DB AND not in defaultValueMap (which is now handled by adding it to the map),
		// or it's another DB error.
		return nil, common.NewErrorf("GetWebSettings: failed to get panelLanguage: %w", err)
	}
	settings["panelLanguage"] = strVal

	strVal, err = s.getString(db, "panelTheme")
	if err != nil {
		return nil, common.NewErrorf("GetWebSettings: failed to get panelTheme: %w", err)
	}
	settings["panelTheme"] = strVal

	// Add other web-specific settings as needed.
	// Example: WebUsername, WebPassword (handle with extreme care, avoid sending plaintext passwords)

	return settings, nil
}

func (s *SettingService) GetSubSettings() (map[string]interface{}, error) {
	db := database.GetDB()
	settings := make(map[string]interface{})
	var err error
	var strVal string
	var intVal int
	var boolVal bool

	strVal, err = s.getString(db, "subListen")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subListen: %w", err)
	}
	settings["subListen"] = strVal

	strVal, err = s.getString(db, "subPort")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subPort: %w", err)
	}
	intVal, err = strconv.Atoi(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to parse subPort: %w", err)
	}
	settings["subPort"] = intVal

	subPath, err := s.GetSubPath() // Formatted path
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subPath: %w", err)
	}
	settings["subPath"] = subPath

	strVal, err = s.getString(db, "subDomain")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subDomain: %w", err)
	}
	settings["subDomain"] = strVal

	strVal, err = s.getString(db, "subCertFile")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subCertFile: %w", err)
	}
	settings["subCertFile"] = strVal

	strVal, err = s.getString(db, "subKeyFile")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subKeyFile: %w", err)
	}
	settings["subKeyFile"] = strVal

	strVal, err = s.getString(db, "subUpdates")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subUpdates: %w", err)
	}
	intVal, err = strconv.Atoi(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to parse subUpdates: %w", err)
	}
	settings["subUpdates"] = intVal

	strVal, err = s.getString(db, "subEncode")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subEncode: %w", err)
	}
	boolVal, err = strconv.ParseBool(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to parse subEncode: %w", err)
	}
	settings["subEncode"] = boolVal

	strVal, err = s.getString(db, "subShowInfo")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subShowInfo: %w", err)
	}
	boolVal, err = strconv.ParseBool(strVal)
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to parse subShowInfo: %w", err)
	}
	settings["subShowInfo"] = boolVal

	strVal, err = s.getString(db, "subURI")
	if err != nil {
		return nil, common.NewErrorf("GetSubSettings: failed to get subURI: %w", err)
	}
	settings["subURI"] = strVal

	strVal, err = s.getString(db, "subJsonExt")
	if err != nil {
		// If getString errors, it means it's not in DB AND not in defaultValueMap (which is now handled for subJsonExt),
		// or it's another DB error.
		return nil, common.NewErrorf("GetSubSettings: failed to get subJsonExt: %w", err)
	}
	settings["subJsonExt"] = strVal

	return settings, nil
}

// GetCoreSettings collects all core-related settings into a map.
// This is a simplified version; the actual core config is a JSON string.
func (s *SettingService) GetCoreSettings() (map[string]interface{}, error) {
	db := database.GetDB()
	settings := make(map[string]interface{})
	var err error

	coreConfigJSON, err := s.getString(db, "config")
	if err != nil {
		return nil, common.NewErrorf("GetCoreSettings: failed to get core config JSON: %w", err)
	}
	settings["coreConfig"] = coreConfigJSON // The raw JSON string for the core

	// Other core-related settings that might be stored individually
	// Example:
	// coreMode, err := s.getString(db, "coreMode") // Assuming "coreMode" is a key
	// if err == nil { settings["coreMode"] = coreMode }
	// else if !common.IsNotFound(err) { return nil, err }

	// For now, primarily returning the main config JSON.
	// Add other individual core settings if they exist as separate key-value pairs.

	return settings, nil
}

// IsPathExists checks if a file or directory exists at the given path.
func IsPathExists(path string) (bool, error) {
	_, err := os.Stat(path) // Added os.
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) { // Added os.
		return false, nil
	}
	return false, err
}
