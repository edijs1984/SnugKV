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

func IsJSONPath(path string) bool {
	return strings.HasPrefix(path, "$")
}

func normalizePath(path string) (string, error) {
	if path == "." {
		return "$", nil
	}
	if strings.HasPrefix(path, ".") {
		return "$" + path, nil
	}
	if path == "" {
		return "", errors.New("ERR invalid JSON path")
	}
	if path[0] != '$' {
		return "$." + path, nil
	}
	return path, nil
}

func parsePath(path string) ([]pathToken, error) {
	var err error
	path, err = normalizePath(path)
	if err != nil {
		return nil, err
	}
	if path == "$" {
		return nil, nil
	}

	var tokens []pathToken
	for i := 1; i < len(path); {
		switch path[i] {
		case '.':
			i++
			if i >= len(path) {
				return nil, errors.New("ERR invalid JSON path")
			}

			if path[i] == '.' {
				i++
				if i >= len(path) || path[i] == '[' {
					return nil, errors.New("ERR invalid JSON path")
				}
				if path[i] == '*' {
					tokens = append(tokens, pathToken{kind: pathRecursiveWildcard})
					i++
					continue
				}
				start := i
				for i < len(path) && path[i] != '.' && path[i] != '[' {
					i++
				}
				if start == i {
					return nil, errors.New("ERR invalid JSON path")
				}
				tokens = append(tokens, pathToken{
					kind:   pathRecursiveMember,
					member: path[start:i],
				})
				continue
			}

			if path[i] == '[' {
				return nil, errors.New("ERR invalid JSON path")
			}
			if path[i] == '*' {
				tokens = append(tokens, pathToken{kind: pathWildcard})
				i++
				continue
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

	if path[i] == '*' {
		i++
		if i >= len(path) || path[i] != ']' {
			return pathToken{}, 0, errors.New("ERR invalid JSON path")
		}
		return pathToken{kind: pathWildcard}, i + 1, nil
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
			decoded, err := strconv.Unquote("\"" + raw + "\"")
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

func Matches(root any, path string) ([]any, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0)
	collectMatches(root, tokens, &out)
	return out, nil
}

func collectMatches(current any, tokens []pathToken, out *[]any) {
	if len(tokens) == 0 {
		*out = append(*out, current)
		return
	}

	token := tokens[0]
	rest := tokens[1:]

	switch token.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return
		}
		child, ok := object[token.member]
		if ok {
			collectMatches(child, rest, out)
		}

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return
		}
		index, ok := resolveIndex(len(array), token.index)
		if ok {
			collectMatches(array[index], rest, out)
		}

	case pathWildcard:
		switch value := current.(type) {
		case []any:
			for _, child := range value {
				collectMatches(child, rest, out)
			}
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				collectMatches(value[key], rest, out)
			}
		}

	case pathRecursiveMember:
		collectRecursiveMember(current, token.member, rest, out)

	case pathRecursiveWildcard:
		collectRecursiveWildcard(current, rest, out)
	}
}

func collectRecursiveMember(current any, member string, rest []pathToken, out *[]any) {
	switch value := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := value[key]
			if key == member {
				collectMatches(child, rest, out)
			}
			collectRecursiveMember(child, member, rest, out)
		}
	case []any:
		for _, child := range value {
			collectRecursiveMember(child, member, rest, out)
		}
	}
}

func collectRecursiveWildcard(current any, rest []pathToken, out *[]any) {
	switch value := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := value[key]
			collectMatches(child, rest, out)
			collectRecursiveWildcard(child, rest, out)
		}
	case []any:
		for _, child := range value {
			collectMatches(child, rest, out)
			collectRecursiveWildcard(child, rest, out)
		}
	}
}

func SetMatches(root any, path string, value any) (any, int, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, 0, err
	}
	if len(tokens) == 0 {
		return value, 1, nil
	}
	updated, count := setMatchesAt(root, tokens, value)
	return updated, count, nil
}

func setMatchesAt(current any, tokens []pathToken, value any) (any, int) {
	token := tokens[0]
	last := len(tokens) == 1

	switch token.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return current, 0
		}
		if last {
			object[token.member] = value
			return current, 1
		}
		child, ok := object[token.member]
		if !ok {
			return current, 0
		}
		updated, count := setMatchesAt(child, tokens[1:], value)
		if count > 0 {
			object[token.member] = updated
		}
		return current, count

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		index, ok := resolveIndex(len(array), token.index)
		if !ok {
			return current, 0
		}
		if last {
			array[index] = value
			return array, 1
		}
		updated, count := setMatchesAt(array[index], tokens[1:], value)
		if count > 0 {
			array[index] = updated
		}
		return array, count

	case pathWildcard:
		switch container := current.(type) {
		case []any:
			count := 0
			if last {
				for i := range container {
					container[i] = value
				}
				return container, len(container)
			}
			for i := range container {
				updated, n := setMatchesAt(container[i], tokens[1:], value)
				if n > 0 {
					container[i] = updated
					count += n
				}
			}
			return container, count

		case map[string]any:
			keys := make([]string, 0, len(container))
			for key := range container {
				keys = append(keys, key)
			}
			sort.Strings(keys)

			count := 0
			if last {
				for _, key := range keys {
					container[key] = value
				}
				return container, len(keys)
			}
			for _, key := range keys {
				updated, n := setMatchesAt(container[key], tokens[1:], value)
				if n > 0 {
					container[key] = updated
					count += n
				}
			}
			return container, count
		}

	case pathRecursiveMember:
		return setRecursiveMember(current, token.member, rest, value)

	case pathRecursiveWildcard:
		return setRecursiveWildcard(current, rest, value)
	}

	return current, 0
}

func setRecursiveMember(current any, member string, rest []pathToken, value any) (any, int) {
	count := 0
	switch container := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(container))
		for key := range container {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := container[key]
			if key == member {
				if len(rest) == 0 {
					container[key] = value
					count++
					continue
				}
				updated, n := setMatchesAt(child, rest, value)
				if n > 0 {
					container[key] = updated
					count += n
					child = updated
				}
			}
			updated, n := setRecursiveMember(child, member, rest, value)
			if n > 0 {
				container[key] = updated
				count += n
			}
		}
	case []any:
		for i := range container {
			updated, n := setRecursiveMember(container[i], member, rest, value)
			if n > 0 {
				container[i] = updated
				count += n
			}
		}
	}
	return current, count
}

func setRecursiveWildcard(current any, rest []pathToken, value any) (any, int) {
	count := 0
	switch container := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(container))
		for key := range container {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := container[key]
			if len(rest) == 0 {
				container[key] = value
				count++
				continue
			}
			updated, n := setMatchesAt(child, rest, value)
			if n > 0 {
				container[key] = updated
				count += n
				child = updated
			}
			updated, n = setRecursiveWildcard(child, rest, value)
			if n > 0 {
				container[key] = updated
				count += n
			}
		}
	case []any:
		for i := range container {
			child := container[i]
			if len(rest) == 0 {
				container[i] = value
				count++
				continue
			}
			updated, n := setMatchesAt(child, rest, value)
			if n > 0 {
				container[i] = updated
				count += n
				child = updated
			}
			updated, n = setRecursiveWildcard(child, rest, value)
			if n > 0 {
				container[i] = updated
				count += n
			}
		}
	}
	return current, count
}

func DeleteMatches(root any, path string) (any, int, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, 0, err
	}
	if len(tokens) == 0 {
		return nil, 1, nil
	}
	updated, count := deleteMatchesAt(root, tokens)
	return updated, count, nil
}

func deleteMatchesAt(current any, tokens []pathToken) (any, int) {
	token := tokens[0]
	last := len(tokens) == 1

	switch token.kind {
	case pathMember:
		object, ok := current.(map[string]any)
		if !ok {
			return current, 0
		}
		if last {
			if _, ok := object[token.member]; !ok {
				return current, 0
			}
			delete(object, token.member)
			return current, 1
		}
		child, ok := object[token.member]
		if !ok {
			return current, 0
		}
		updated, count := deleteMatchesAt(child, tokens[1:])
		if count > 0 {
			object[token.member] = updated
		}
		return current, count

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		index, ok := resolveIndex(len(array), token.index)
		if !ok {
			return current, 0
		}
		if last {
			array = append(array[:index], array[index+1:]...)
			return array, 1
		}
		updated, count := deleteMatchesAt(array[index], tokens[1:])
		if count > 0 {
			array[index] = updated
		}
		return array, count

	case pathWildcard:
		switch container := current.(type) {
		case []any:
			if last {
				return []any{}, len(container)
			}
			count := 0
			for i := range container {
				updated, n := deleteMatchesAt(container[i], tokens[1:])
				if n > 0 {
					container[i] = updated
					count += n
				}
			}
			return container, count

		case map[string]any:
			keys := make([]string, 0, len(container))
			for key := range container {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if last {
				count := len(keys)
				for _, key := range keys {
					delete(container, key)
				}
				return container, count
			}
			count := 0
			for _, key := range keys {
				updated, n := deleteMatchesAt(container[key], tokens[1:])
				if n > 0 {
					container[key] = updated
					count += n
				}
			}
			return container, count
		}

	case pathRecursiveMember:
		return deleteRecursiveMember(current, token.member, rest)

	case pathRecursiveWildcard:
		return deleteRecursiveWildcard(current, rest)
	}

	return current, 0
}

func deleteRecursiveMember(current any, member string, rest []pathToken) (any, int) {
	count := 0
	switch container := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(container))
		for key := range container {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			child := container[key]
			if key == member {
				if len(rest) == 0 {
					delete(container, key)
					count++
					continue
				}
				updated, n := deleteMatchesAt(child, rest)
				if n > 0 {
					container[key] = updated
					count += n
					child = updated
				}
			}
			updated, n := deleteRecursiveMember(child, member, rest)
			if n > 0 {
				container[key] = updated
				count += n
			}
		}

	case []any:
		for i := range container {
			updated, n := deleteRecursiveMember(container[i], member, rest)
			if n > 0 {
				container[i] = updated
				count += n
			}
		}
	}
	return current, count
}

func deleteRecursiveWildcard(current any, rest []pathToken) (any, int) {
	count := 0
	switch container := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(container))
		for key := range container {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(rest) == 0 {
			for _, key := range keys {
				delete(container, key)
			}
			return container, len(keys)
		}
		for _, key := range keys {
			child := container[key]
			updated, n := deleteMatchesAt(child, rest)
			if n > 0 {
				container[key] = updated
				count += n
				child = updated
			}
			updated, n = deleteRecursiveWildcard(child, rest)
			if n > 0 {
				container[key] = updated
				count += n
			}
		}

	case []any:
		if len(rest) == 0 {
			return []any{}, len(container)
		}
		for i := range container {
			child := container[i]
			updated, n := deleteMatchesAt(child, rest)
			if n > 0 {
				container[i] = updated
				count += n
				child = updated
			}
			updated, n = deleteRecursiveWildcard(child, rest)
			if n > 0 {
				container[i] = updated
				count += n
			}
		}
	}
	return current, count
}
