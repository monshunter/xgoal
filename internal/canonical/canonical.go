package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const hashPrefix = "xgoal-canonical/v1\n"

func Marshal(value any) ([]byte, error) {
	if err := validateSource(reflect.ValueOf(value), make(map[visit]bool)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("decode JSON model: %w", err)
	}
	var buffer bytes.Buffer
	if err := writeValue(&buffer, normalized); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func Hash(kind, schemaVersion string, value any) (string, error) {
	if kind == "" || schemaVersion == "" || strings.ContainsAny(kind+schemaVersion, "\r\n") {
		return "", fmt.Errorf("kind and schema version must be non-empty single-line values")
	}
	canonical, err := Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write([]byte(hashPrefix))
	hash.Write([]byte(kind))
	hash.Write([]byte("\n"))
	hash.Write([]byte(schemaVersion))
	hash.Write([]byte("\n"))
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type visit struct {
	typeName reflect.Type
	pointer  uintptr
}

func validateSource(value reflect.Value, seen map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.CanInterface() {
		if _, ok := value.Interface().(json.Marshaler); ok {
			return nil
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			key := visit{typeName: value.Type(), pointer: value.Pointer()}
			if seen[key] {
				return fmt.Errorf("cyclic values are not canonicalizable")
			}
			seen[key] = true
			defer delete(seen, key)
		}
		return validateSource(value.Elem(), seen)
	case reflect.Float32, reflect.Float64:
		return fmt.Errorf("floating point values are forbidden in canonical v1 objects")
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("strings must be valid UTF-8")
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("map keys must be strings")
		}
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if seen[key] {
			return fmt.Errorf("cyclic values are not canonicalizable")
		}
		seen[key] = true
		defer delete(seen, key)
		iterator := value.MapRange()
		for iterator.Next() {
			if !utf8.ValidString(iterator.Key().String()) {
				return fmt.Errorf("map keys must be valid UTF-8")
			}
			if err := validateSource(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() || value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if seen[key] {
			return fmt.Errorf("cyclic values are not canonicalizable")
		}
		seen[key] = true
		defer delete(seen, key)
		for i := 0; i < value.Len(); i++ {
			if err := validateSource(value.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateSource(value.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := validateSource(value.Field(i), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeValue(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		writeString(buffer, typed)
	case json.Number:
		if !isCanonicalInteger(typed.String()) {
			return fmt.Errorf("non-integer number %q is forbidden in canonical v1 objects", typed)
		}
		buffer.WriteString(typed.String())
	case []any:
		buffer.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buffer.WriteByte(',')
			}
			if err := writeValue(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		buffer.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buffer.WriteByte(',')
			}
			writeString(buffer, key)
			buffer.WriteByte(':')
			if err := writeValue(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func writeString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			buffer.WriteByte('\\')
			buffer.WriteRune(character)
		case '\b':
			buffer.WriteString("\\b")
		case '\t':
			buffer.WriteString("\\t")
		case '\n':
			buffer.WriteString("\\n")
		case '\f':
			buffer.WriteString("\\f")
		case '\r':
			buffer.WriteString("\\r")
		default:
			if character < 0x20 {
				fmt.Fprintf(buffer, "\\u%04x", character)
			} else {
				buffer.WriteRune(character)
			}
		}
	}
	buffer.WriteByte('"')
}

func utf16Less(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := len(leftUnits)
	if len(rightUnits) < limit {
		limit = len(rightUnits)
	}
	for i := 0; i < limit; i++ {
		if leftUnits[i] != rightUnits[i] {
			return leftUnits[i] < rightUnits[i]
		}
	}
	return len(leftUnits) < len(rightUnits)
}

func isCanonicalInteger(value string) bool {
	if value == "0" {
		return true
	}
	if strings.HasPrefix(value, "-") {
		value = value[1:]
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
