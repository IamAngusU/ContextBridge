// Package strictjson decodes closed JSON documents without the ambiguous
// behaviours that encoding/json keeps for backwards compatibility.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var (
	rawMessageType  = reflect.TypeOf(json.RawMessage{})
	unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
)

// Decode rejects invalid UTF-8, duplicate object properties, case-folded
// aliases of declared struct fields, unknown struct fields, and trailing JSON.
// Map keys and json.RawMessage contents remain case-sensitive opaque data.
func Decode(raw []byte, target any) error {
	if err := validate(raw, reflect.TypeOf(target)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

// Validate checks UTF-8 and JSON syntax and rejects duplicate properties
// recursively.
func Validate(raw []byte) error {
	return validate(raw, nil)
}

func validate(raw []byte, target reflect.Type) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanUniqueValue(decoder, target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func scanUniqueValue(decoder *json.Decoder, target reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		fields, mapValue := schemaObject(target)
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON property %q", key)
			}
			seen[key] = struct{}{}
			childType := mapValue
			if fields != nil {
				var exact bool
				childType, exact = fields[key]
				if !exact {
					for name := range fields {
						if strings.EqualFold(key, name) {
							return fmt.Errorf("JSON property %q must use exact spelling %q", key, name)
						}
					}
					childType = nil
				}
			}
			if err := scanUniqueValue(decoder, childType); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		elementType := schemaArrayElement(target)
		for decoder.More() {
			if err := scanUniqueValue(decoder, elementType); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func schemaType(target reflect.Type) reflect.Type {
	if target == nil {
		return nil
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == rawMessageType || reflect.PointerTo(target).Implements(unmarshalerType) {
		return nil
	}
	return target
}

func schemaObject(target reflect.Type) (map[string]reflect.Type, reflect.Type) {
	target = schemaType(target)
	if target == nil {
		return nil, nil
	}
	switch target.Kind() {
	case reflect.Struct:
		return jsonFields(target), nil
	case reflect.Map:
		return nil, target.Elem()
	}
	return nil, nil
}

func schemaArrayElement(target reflect.Type) reflect.Type {
	target = schemaType(target)
	if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) {
		return target.Elem()
	}
	return nil
}

func jsonFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	collectJSONFields(target, fields, make(map[reflect.Type]bool))
	return fields
}

func collectJSONFields(target reflect.Type, fields map[string]reflect.Type, visiting map[reflect.Type]bool) {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct || visiting[target] {
		return
	}
	visiting[target] = true
	defer delete(visiting, target)
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag, tagged := field.Tag.Lookup("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if field.Anonymous && (!tagged || name == "") {
			collectJSONFields(field.Type, fields, visiting)
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
}
