package model

import (
	"encoding/json"
	"fmt"
)

type Outbound struct {
	Id      uint            `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Type    string          `json:"type" form:"type"`
	Tag     string          `json:"tag" form:"tag" gorm:"unique"`
	Options json.RawMessage `json:"-" form:"-"`
}

func (o *Outbound) UnmarshalJSON(data []byte) error {
	var err error
	var raw map[string]interface{}
	if err = json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to unmarshal outbound data: %w", err)
	}

	// Extract fixed fields and store the rest in Options
	if idVal, exists := raw["id"]; exists {
		if idFloat, ok := idVal.(float64); ok {
			o.Id = uint(idFloat)
		} else if idVal != nil { // only error if id is present but not a number
			return fmt.Errorf("id field is not a valid number: %v", raw["id"])
		}
	}
	delete(raw, "id")

	if typeVal, exists := raw["type"]; exists {
		if typeStr, ok := typeVal.(string); ok {
			o.Type = typeStr
		} else if typeVal != nil { // only treat as issue if type is present but not a string
			// Defaulting to empty string if type is not a string or missing
			o.Type = ""
		}
	} else {
		o.Type = "" // Default to empty if missing
	}
	delete(raw, "type")

	tagVal, tagExists := raw["tag"]
	if !tagExists {
		return fmt.Errorf("tag field is missing")
	}
	tagStr, tagIsString := tagVal.(string)
	if !tagIsString {
		return fmt.Errorf("tag field is not a string: %v", tagVal)
	}
	if tagStr == "" {
		return fmt.Errorf("tag field cannot be empty")
	}
	o.Tag = tagStr
	delete(raw, "tag")

	// Remaining fields
	if len(raw) > 0 {
		o.Options, err = json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal outbound options: %w", err)
		}
	} else {
		o.Options = nil // Explicitly set to nil if no options
	}
	return nil
}

// MarshalJSON customizes marshalling
func (o Outbound) MarshalJSON() ([]byte, error) {
	// Combine fixed fields and dynamic fields into one map
	combined := make(map[string]interface{})
	combined["type"] = o.Type
	combined["tag"] = o.Tag

	if o.Options != nil {
		var restFields map[string]json.RawMessage
		if err := json.Unmarshal(o.Options, &restFields); err != nil {
			return nil, err
		}

		for k, v := range restFields {
			combined[k] = v
		}
	}

	return json.Marshal(combined)
}
