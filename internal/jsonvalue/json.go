package jsonvalue

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

type pathTokenKind uint8

const (
	pathMember pathTokenKind = iota
	pathIndex
	pathWildcard
	pathRecursiveMember
	pathRecursiveWildcard
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

func IsJSONPath(path string) bool {
	return strings.HasPrefix(path, "$")
}

func parsePath(path string) ([]pathToken, error) {
	if path == "$" || path == "." {
		return nil, nil
	}
	if path == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	pos := 0
	if path[0] == '$' {
		pos = 1
	} else if path[0] == '.' {
		pos = 1
	}

	var tokens []pathToken

	for pos < len(path) {
		switch path[pos] {
		case '.':
			if pos+1 < len(path) && path[pos+1] == '.' {
				pos += 2
				if pos >= len(path) {
					return nil, errors.New("ERR invalid JSON path")
				}
				if path[pos] == '*' {
					tokens = append(tokens, pathToken{kind: pathRecursiveWildcard})
					pos++
					continue
				}
				name, next := bareMember(path, pos)
				if name == "" {
					return nil, errors.New("ERR invalid JSON path")
				}
				tokens = append(tokens, pathToken{kind: pathRecursiveMember, member: name})
				pos = next
				continue
			}

			pos++
			// RedisJSON accepts paths such as $.[0].
			if pos < len(path) && path[pos] == '[' {
				continue
			}
			if pos >= len(path) {
				return nil, errors.New("ERR invalid JSON path")
			}
			if path[pos] == '*' {
				tokens = append(tokens, pathToken{kind: pathWildcard})
				pos++
				continue
			}
			name, next := bareMember(path, pos)
			if name == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			tokens = append(tokens, pathToken{kind: pathMember, member: name})
			pos = next

		case '[':
			token, next, err := bracketToken(path, pos)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
			pos = next

		default:
			name, next := bareMember(path, pos)
			if name == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			tokens = append(tokens, pathToken{kind: pathMember, member: name})
			pos = next
		}
	}

	return tokens, nil
}

func bareMember(path string, pos int) (string, int) {
	start := pos
	for pos < len(path) && path[pos] != '.' && path[pos] != '[' {
		pos++
	}
	return path[start:pos], pos
}

func bracketToken(path string, pos int) (pathToken, int, error) {
	end := pos + 1
	quote := byte(0)

	for end < len(path) {
		c := path[end]
		if quote != 0 {
			if c == '\\' {
				end += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			end++
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			end++
			continue
		}
		if c == ']' {
			break
		}
		end++
	}

	if end >= len(path) || quote != 0 {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	raw := strings.TrimSpace(path[pos+1 : end])
	if raw == "" {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	if raw == "*" {
		return pathToken{kind: pathWildcard}, end + 1, nil
	}

	if (raw[0] == '"' && raw[len(raw)-1] == '"') ||
		(raw[0] == '\'' && raw[len(raw)-1] == '\'') {
		name, err := unquoteMember(raw)
		if err != nil {
			return pathToken{}, 0, errors.New("ERR invalid JSON path")
		}
		return pathToken{kind: pathMember, member: name}, end + 1, nil
	}

	index, err := strconv.Atoi(raw)
	if err != nil {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	return pathToken{kind: pathIndex, index: index}, end + 1, nil
}

func unquoteMember(raw string) (string, error) {
	if raw[0] == '"' {
		return strconv.Unquote(raw)
	}

	body := raw[1 : len(raw)-1]
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			out.WriteByte(body[i])
			continue
		}
		i++
		if i >= len(body) {
			return "", errors.New("invalid escape")
		}
		switch body[i] {
		case '\\', '\'':
			out.WriteByte(body[i])
		default:
			out.WriteByte(body[i])
		}
	}
	return out.String(), nil
}


func sortedObjectKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeIndex(index, length int) (int, bool) {
	if index < 0 {
		index += length
	}
	return index, index >= 0 && index < length
}

// GetAll evaluates both modern ($...) JSONPath syntax and legacy paths. The
// returned slice contains every match in traversal order.
func GetAll(root any, path string) ([]any, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}

	current := []any{root}
	for _, token := range tokens {
		next := make([]any, 0)
		for _, value := range current {
			applyQueryToken(value, token, &next)
		}
		current = next
		if len(current) == 0 {
			break
		}
	}
	return current, nil
}

func applyQueryToken(value any, token pathToken, out *[]any) {
	switch token.kind {
	case pathMember:
		if object, ok := value.(map[string]any); ok {
			if child, exists := object[token.member]; exists {
				*out = append(*out, child)
			}
		}

	case pathIndex:
		if array, ok := value.([]any); ok {
			if index, valid := normalizeIndex(token.index, len(array)); valid {
				*out = append(*out, array[index])
			}
		}

	case pathWildcard:
		switch container := value.(type) {
		case []any:
			*out = append(*out, container...)
		case map[string]any:
			for _, key := range sortedObjectKeys(container) {
				*out = append(*out, container[key])
			}
		}

	case pathRecursiveMember:
		collectRecursiveMember(value, token.member, out)

	case pathRecursiveWildcard:
		collectRecursiveWildcard(value, out)
	}
}

func collectRecursiveMember(value any, name string, out *[]any) {
	switch container := value.(type) {
	case map[string]any:
		if child, exists := container[name]; exists {
			*out = append(*out, child)
		}
		for _, key := range sortedObjectKeys(container) {
			collectRecursiveMember(container[key], name, out)
		}
	case []any:
		for _, child := range container {
			collectRecursiveMember(child, name, out)
		}
	}
}

func collectRecursiveWildcard(value any, out *[]any) {
	switch container := value.(type) {
	case map[string]any:
		for _, key := range sortedObjectKeys(container) {
			child := container[key]
			*out = append(*out, child)
			collectRecursiveWildcard(child, out)
		}
	case []any:
		for _, child := range container {
			*out = append(*out, child)
			collectRecursiveWildcard(child, out)
		}
	}
}

// Get preserves the original single-value helper API. For paths with multiple
// matches it returns the first match; JSON commands that need JSONPath collection
// semantics should use GetAll.
func Get(root any, path string) (any, bool, error) {
	values, err := GetAll(root, path)
	if err != nil {
		return nil, false, err
	}
	if len(values) == 0 {
		return nil, false, nil
	}
	return values[0], true, nil
}

func Set(root any, path string, value any) (any, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}

	if len(tokens) == 0 {
		return value, nil
	}

	for _, token := range tokens {
		if token.kind == pathWildcard ||
			token.kind == pathRecursiveMember ||
			token.kind == pathRecursiveWildcard {
			return nil, errors.New("ERR JSON path is not static")
		}
	}

	current := root
	for i := 0; i < len(tokens)-1; i++ {
		token := tokens[i]
		switch token.kind {
		case pathMember:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, errors.New("ERR JSON path does not exist")
			}
			next, exists := object[token.member]
			if !exists {
				return nil, errors.New("ERR JSON path does not exist")
			}
			current = next

		case pathIndex:
			array, ok := current.([]any)
			if !ok {
				return nil, errors.New("ERR JSON path does not exist")
			}
			index, valid := normalizeIndex(token.index, len(array))
			if !valid {
				return nil, errors.New("ERR index out of bounds")
			}
			current = array[index]
		}
	}

	last := tokens[len(tokens)-1]
	switch last.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("ERR JSON path does not exist")
		}
		object[last.member] = value
		return root, nil

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return nil, errors.New("ERR JSON path does not exist")
		}
		index, valid := normalizeIndex(last.index, len(array))
		if !valid {
			return nil, errors.New("ERR index out of bounds")
		}
		array[index] = value
		return root, nil
	}

	return nil, errors.New("ERR JSON path is not static")
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

	for _, token := range tokens {
		if token.kind == pathWildcard ||
			token.kind == pathRecursiveMember ||
			token.kind == pathRecursiveWildcard {
			return nil, false, errors.New("ERR JSON path is not static")
		}
	}

	current := root
	for i := 0; i < len(tokens)-1; i++ {
		token := tokens[i]
		switch token.kind {
		case pathMember:
			object, ok := current.(map[string]any)
			if !ok {
				return root, false, nil
			}
			next, exists := object[token.member]
			if !exists {
				return root, false, nil
			}
			current = next

		case pathIndex:
			array, ok := current.([]any)
			if !ok {
				return root, false, nil
			}
			index, valid := normalizeIndex(token.index, len(array))
			if !valid {
				return root, false, nil
			}
			current = array[index]
		}
	}

	last := tokens[len(tokens)-1]
	switch last.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return root, false, nil
		}
		if _, exists := object[last.member]; !exists {
			return root, false, nil
		}
		delete(object, last.member)
		return root, true, nil

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return root, false, nil
		}
		index, valid := normalizeIndex(last.index, len(array))
		if !valid {
			return root, false, nil
		}
		array = append(array[:index], array[index+1:]...)
		if len(tokens) == 1 {
			return array, true, nil
		}

		// Reattach the modified slice because append can change its header.
		parent, err := setContainer(root, tokens[:len(tokens)-1], array)
		if err != nil {
			return nil, false, err
		}
		return parent, true, nil
	}

	return root, false, errors.New("ERR JSON path is not static")
}

func setContainer(root any, tokens []pathToken, replacement any) (any, error) {
	if len(tokens) == 0 {
		return replacement, nil
	}

	current := root
	for i := 0; i < len(tokens)-1; i++ {
		token := tokens[i]
		switch token.kind {
		case pathMember:
			current = current.(map[string]any)[token.member]
		case pathIndex:
			array := current.([]any)
			index, _ := normalizeIndex(token.index, len(array))
			current = array[index]
		default:
			return nil, errors.New("ERR JSON path is not static")
		}
	}

	last := tokens[len(tokens)-1]
	switch last.kind {
	case pathMember:
		current.(map[string]any)[last.member] = replacement
	case pathIndex:
		array := current.([]any)
		index, _ := normalizeIndex(last.index, len(array))
		array[index] = replacement
	default:
		return nil, errors.New("ERR JSON path is not static")
	}

	return root, nil
}
