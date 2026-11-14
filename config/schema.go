package config

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/TwiN/gatus/v5/alerting/provider/bark"
	"gopkg.in/yaml.v3"
)

// Schema describes the YAML configuration using JSON Schema draft 7. It is
// derived from the same YAML tags used by the loader, including inline providers
// and maps, so new configuration fields automatically appear in the editor.
type Schema struct {
	Dialect              string             `json:"$schema,omitempty"`
	Type                 []string           `json:"type,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
}

func ConfigurationSchema() *Schema {
	s := schemaFor(reflect.TypeOf(Config{}), false)
	s.Dialect = "http://json-schema.org/draft-07/schema#"
	return s
}

func schemaFor(t reflect.Type, nullable bool) *Schema {
	if t.Kind() == reflect.Pointer {
		return schemaFor(t.Elem(), nullable)
	}
	s := &Schema{}
	switch t {
	case reflect.TypeOf(time.Duration(0)):
		s.Type = []string{"string"}
		s.Pattern = `^[-+]?(0|([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))*)$`
	case reflect.TypeOf(time.Time{}):
		s.Type = []string{"string"}
	case reflect.TypeOf(bark.Override{}):
		// Bark has a custom YAML unmarshaler: Group and Config have yaml:"-"
		// tags on the Go struct, but form a flat mapping in the YAML document.
		s = schemaFor(reflect.TypeOf(bark.Config{}), false)
		s.Properties["group"] = &Schema{Type: []string{"string"}}
	default:
		switch t.Kind() {
		case reflect.Struct:
			s.Type = []string{"object"}
			s.Properties = make(map[string]*Schema)
			s.AdditionalProperties = false
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() || f.Tag.Get("yaml") == "-" {
					continue
				}
				tag := strings.Split(f.Tag.Get("yaml"), ",")
				child := schemaFor(f.Type, true)
				if len(tag) > 1 && tag[1] == "inline" {
					for name, property := range child.Properties {
						s.Properties[name] = property
					}
					if f.Type.Kind() == reflect.Map {
						s.AdditionalProperties = child.AdditionalProperties
					}
					continue
				}
				name := tag[0]
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				s.Properties[name] = child
			}
		case reflect.Map:
			s.Type = []string{"object"}
			s.AdditionalProperties = schemaFor(t.Elem(), false)
		case reflect.Slice, reflect.Array:
			s.Type = []string{"array"}
			s.Items = schemaFor(t.Elem(), false)
		case reflect.String:
			s.Type = []string{"string"}
		case reflect.Bool:
			s.Type = []string{"boolean"}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			s.Type = []string{"integer"}
		case reflect.Float32, reflect.Float64:
			s.Type = []string{"number"}
		}
	}
	if nullable && len(s.Type) > 0 {
		s.Type = append(s.Type, "null")
	}
	return s
}

// ValidateEditorYAML checks structure before startup validation. Unlike the
// permissive startup decoder, it rejects unknown keys and scalar coercions.
// Only one YAML document is accepted: the loader reads one document per file.
func ValidateEditorYAML(content []byte) error {
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	var data any
	if err := decoder.Decode(&data); err != nil {
		if err == io.EOF {
			return nil // Empty fragments are valid in a configuration directory.
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected exactly one YAML document")
	}
	// Normalize YAML timestamps and reject non-string mapping keys.
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("configuration must use string mapping keys")
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	return ConfigurationSchema().validate(data, "config")
}

func (s *Schema) validate(value any, path string) error {
	if len(s.Type) == 0 {
		return nil // interface{} values, e.g. suite context
	}
	typeName := "null"
	switch v := value.(type) {
	case map[string]any:
		typeName = "object"
		for key, child := range v {
			property := s.Properties[key]
			if property == nil {
				property, _ = s.AdditionalProperties.(*Schema)
			}
			if property == nil {
				return fmt.Errorf("%s.%s: unknown configuration key", path, key)
			}
			if err := property.validate(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		typeName = "array"
		if s.Items != nil {
			for i, child := range v {
				if err := s.Items.validate(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		typeName = "string"
	case bool:
		typeName = "boolean"
	case float64:
		typeName = "number"
		if v == float64(int64(v)) {
			typeName = "integer"
		}
	}
	for _, allowed := range s.Type {
		if typeName == allowed || (typeName == "integer" && allowed == "number") {
			if s.Pattern != "" && typeName == "string" {
				if _, err := time.ParseDuration(value.(string)); err != nil {
					return fmt.Errorf("%s: expected a duration such as 30s or 5m", path)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("%s: expected %s, got %s", path, strings.Join(s.Type, " or "), typeName)
}
