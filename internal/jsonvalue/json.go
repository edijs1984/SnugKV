package jsonvalue

import (
	"encoding/json"
	"errors"
	"strings"
)

func Parse(raw []byte) (any, error) {
	var value any

	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("ERR invalid JSON")
	}

	return value, nil
}

func Encode(value any) ([]byte, error) {
	return json.Marshal(value)
}

func PathParts(path string) ([]string, error) {
	if path == "$" {
		return nil, nil
	}

	if !strings.HasPrefix(path, "$.") {
		return nil, errors.New("ERR invalid JSON path")
	}

	raw := strings.TrimPrefix(path, "$.")
	if raw == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	parts := strings.Split(raw, ".")

	for _, part := range parts {
		if part == "" {
			return nil, errors.New("ERR invalid JSON path")
		}
	}

	return parts, nil
}

func Get(root any, path string) (any, bool, error) {
	parts, err := PathParts(path)
	if err != nil {
		return nil, false, err
	}

	if len(parts) == 0 {
		return root, true, nil
	}

	current := root

	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}

		value, exists := object[part]
		if !exists {
			return nil, false, nil
		}

		current = value
	}

	return current, true, nil
}

func Set(root any, path string, value any) (any, error) {
	parts, err := PathParts(path)
	if err != nil {
		return nil, err
	}

	if len(parts) == 0 {
		return value, nil
	}

	object, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("ERR JSON path does not exist")
	}

	current := object

	for i := 0; i < len(parts)-1; i++ {
		next, exists := current[parts[i]]
		if !exists {
			return nil, errors.New("ERR JSON path does not exist")
		}

		child, ok := next.(map[string]any)
		if !ok {
			return nil, errors.New("ERR JSON path does not exist")
		}

		current = child
	}

	current[parts[len(parts)-1]] = value

	return root, nil
}

func TypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if v == float64(int64(v)) {
			return "integer"
		}
		return "number"
	default:
		return "unknown"
	}
}

func Delete(root any, path string) (any, bool, error) {
	parts, err := PathParts(path)
	if err != nil {
		return nil, false, err
	}

	if len(parts) == 0 {
		return nil, true, nil
	}

	object, ok := root.(map[string]any)
	if !ok {
		return root, false, nil
	}

	current := object

	for i := 0; i < len(parts)-1; i++ {
		next, exists := current[parts[i]]
		if !exists {
			return root, false, nil
		}

		child, ok := next.(map[string]any)
		if !ok {
			return root, false, nil
		}

		current = child
	}

	last := parts[len(parts)-1]

	if _, exists := current[last]; !exists {
		return root, false, nil
	}

	delete(current, last)

	return root, true, nil
}
