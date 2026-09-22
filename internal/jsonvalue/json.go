package jsonvalue

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

type pathTokenKind uint8

const (
	pathMember pathTokenKind = iota
	pathIndex
)

type pathToken struct {
	kind   pathTokenKind
	member string
	index  int
}

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

// PathParts is retained for callers/tests that only need object-member paths.
// Array-index paths are represented internally by parsePath.
func PathParts(path string) ([]string, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token.kind != pathMember {
			return nil, errors.New("ERR JSON path contains an array index")
		}
		parts = append(parts, token.member)
	}
	return parts, nil
}

func parsePath(path string) ([]pathToken, error) {
	if path == "$" || path == "." {
		return nil, nil
	}
	if path == "" || path[0] != '$' {
		return nil, errors.New("ERR invalid JSON path")
	}

	var tokens []pathToken
	for i := 1; i < len(path); {
		switch path[i] {
		case '.':
			i++
			if i >= len(path) || path[i] == '.' || path[i] == '[' {
				return nil, errors.New("ERR invalid JSON path")
			}
			start := i
			for i < len(path) && path[i] != '.' && path[i] != '[' {
				i++
			}
			if start == i {
				return nil, errors.New("ERR invalid JSON path")
			}
			tokens = append(tokens, pathToken{
				kind:   pathMember,
				member: path[start:i],
			})

		case '[':
			token, next, err := parseBracketToken(path, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
			i = next

		default:
			return nil, errors.New("ERR invalid JSON path")
		}
	}

	return tokens, nil
}

func parseBracketToken(path string, start int) (pathToken, int, error) {
	i := start + 1
	if i >= len(path) {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	if path[i] == '"' || path[i] == '\'' {
		quote := path[i]
		i++
		valueStart := i
		escaped := false
		for i < len(path) {
			if escaped {
				escaped = false
				i++
				continue
			}
			if path[i] == '\\' {
				escaped = true
				i++
				continue
			}
			if path[i] == quote {
				break
			}
			i++
		}
		if i >= len(path) || path[i] != quote {
			return pathToken{}, 0, errors.New("ERR invalid JSON path")
		}

		raw := path[valueStart:i]
		var member string
		if quote == '"' {
			decoded, err := strconv.Unquote("\\\"" + raw + "\\\"")
			if err != nil {
				return pathToken{}, 0, errors.New("ERR invalid JSON path")
			}
			member = decoded
		} else {
			member = strings.ReplaceAll(raw, "\\'", "'")
			member = strings.ReplaceAll(member, "\\\\", "\\")
		}

		i++
		if i >= len(path) || path[i] != ']' {
			return pathToken{}, 0, errors.New("ERR invalid JSON path")
		}
		return pathToken{kind: pathMember, member: member}, i + 1, nil
	}

	indexStart := i
	if path[i] == '-' {
		i++
	}
	digitStart := i
	for i < len(path) && path[i] >= '0' && path[i] <= '9' {
		i++
	}
	if digitStart == i || i >= len(path) || path[i] != ']' {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	index, err := strconv.Atoi(path[indexStart:i])
	if err != nil {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}
	return pathToken{kind: pathIndex, index: index}, i + 1, nil
}

func resolveIndex(length, index int) (int, bool) {
	if index < 0 {
		index = length + index
	}
	if index < 0 || index >= length {
		return 0, false
	}
	return index, true
}

func Get(root any, path string) (any, bool, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}

	current := root
	for _, token := range tokens {
		switch token.kind {
		case pathMember:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			value, exists := object[token.member]
			if !exists {
				return nil, false, nil
			}
			current = value

		case pathIndex:
			array, ok := current.([]any)
			if !ok {
				return nil, false, nil
			}
			index, ok := resolveIndex(len(array), token.index)
			if !ok {
				return nil, false, nil
			}
			current = array[index]
		}
	}

	return current, true, nil
}

func Set(root any, path string, value any) (any, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return value, nil
	}

	updated, ok, err := setAt(root, tokens, value)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("ERR JSON path does not exist")
	}
	return updated, nil
}

func setAt(current any, tokens []pathToken, value any) (any, bool, error) {
	token := tokens[0]
	last := len(tokens) == 1

	switch token.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return current, false, nil
		}
		if last {
			object[token.member] = value
			return current, true, nil
		}

		child, exists := object[token.member]
		if !exists {
			return current, false, nil
		}
		updated, ok, err := setAt(child, tokens[1:], value)
		if err != nil || !ok {
			return current, ok, err
		}
		object[token.member] = updated
		return current, true, nil

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return current, false, nil
		}
		index, ok := resolveIndex(len(array), token.index)
		if !ok {
			return current, false, nil
		}
		if last {
			array[index] = value
			return array, true, nil
		}

		updated, ok, err := setAt(array[index], tokens[1:], value)
		if err != nil || !ok {
			return current, ok, err
		}
		array[index] = updated
		return array, true, nil
	}

	return current, false, nil
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
	tokens, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return nil, true, nil
	}
	return deleteAt(root, tokens)
}

func deleteAt(current any, tokens []pathToken) (any, bool, error) {
	token := tokens[0]
	last := len(tokens) == 1

	switch token.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return current, false, nil
		}
		if last {
			if _, exists := object[token.member]; !exists {
				return current, false, nil
			}
			delete(object, token.member)
			return current, true, nil
		}

		child, exists := object[token.member]
		if !exists {
			return current, false, nil
		}
		updated, deleted, err := deleteAt(child, tokens[1:])
		if err != nil || !deleted {
			return current, deleted, err
		}
		object[token.member] = updated
		return current, true, nil

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return current, false, nil
		}
		index, ok := resolveIndex(len(array), token.index)
		if !ok {
			return current, false, nil
		}
		if last {
			array = append(array[:index], array[index+1:]...)
			return array, true, nil
		}

		updated, deleted, err := deleteAt(array[index], tokens[1:])
		if err != nil || !deleted {
			return current, deleted, err
		}
		array[index] = updated
		return array, true, nil
	}

	return current, false, nil
}
