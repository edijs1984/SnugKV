package jsonvalue

import "sort"

// Location is the concrete path of one match: only object members and array
// indexes, so a value found with a JSONPath query can be read or replaced again.
type Location []pathToken

type located struct {
	loc   Location
	value any
}

// Locate returns the concrete location of every value a JSONPath query matches,
// in the same order Matches returns the values.
func Locate(root any, path string) ([]Location, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	var found []located
	locateMatches(root, tokens, nil, &found)
	out := make([]Location, len(found))
	for i := range found {
		out[i] = found[i].loc
	}
	return out, nil
}

// GetAt returns the value at a location from Locate.
func GetAt(root any, loc Location) (any, bool) {
	current := root
	for _, token := range loc {
		switch token.kind {
		case pathMember:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			value, exists := object[token.member]
			if !exists {
				return nil, false
			}
			current = value
		case pathIndex:
			array, ok := current.([]any)
			if !ok || token.index < 0 || token.index >= len(array) {
				return nil, false
			}
			current = array[token.index]
		default:
			return nil, false
		}
	}
	return current, true
}

// SetAt replaces the value at a location from Locate and returns the new root.
func SetAt(root any, loc Location, value any) (any, bool) {
	if len(loc) == 0 {
		return value, true
	}
	updated, ok, err := setAt(root, loc, value)
	if err != nil || !ok {
		return root, false
	}
	return updated, true
}

func step(loc Location, token pathToken) Location {
	next := make(Location, len(loc)+1)
	copy(next, loc)
	next[len(loc)] = token
	return next
}

func memberStep(name string) pathToken { return pathToken{kind: pathMember, member: name} }
func indexStep(i int) pathToken        { return pathToken{kind: pathIndex, index: i} }

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// locateMatches mirrors collectMatches, recording where each match was found.
func locateMatches(current any, tokens []pathToken, loc Location, out *[]located) {
	if len(tokens) == 0 {
		*out = append(*out, located{loc: loc, value: current})
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
		if child, ok := object[token.member]; ok {
			locateMatches(child, rest, step(loc, memberStep(token.member)), out)
		}

	case pathIndex:
		array, ok := current.([]any)
		if !ok {
			return
		}
		if index, ok := resolveIndex(len(array), token.index); ok {
			locateMatches(array[index], rest, step(loc, indexStep(index)), out)
		}

	case pathSlice:
		array, ok := current.([]any)
		if !ok {
			return
		}
		for _, index := range sliceIndices(len(array), token.sliceStart, token.sliceEnd, token.sliceStep) {
			locateMatches(array[index], rest, step(loc, indexStep(index)), out)
		}

	case pathUnion:
		array, ok := current.([]any)
		if !ok {
			return
		}
		for _, index := range unionIndices(len(array), token.indices) {
			locateMatches(array[index], rest, step(loc, indexStep(index)), out)
		}

	case pathFilter:
		switch container := current.(type) {
		case []any:
			for _, index := range filteredArrayIndices(container, token.filter) {
				locateMatches(container[index], rest, step(loc, indexStep(index)), out)
			}
		case map[string]any:
			for _, key := range sortedKeys(container) {
				if matchesFilter(container[key], token.filter) {
					locateMatches(container[key], rest, step(loc, memberStep(key)), out)
				}
			}
		}

	case pathWildcard:
		switch value := current.(type) {
		case []any:
			for i, child := range value {
				locateMatches(child, rest, step(loc, indexStep(i)), out)
			}
		case map[string]any:
			for _, key := range sortedKeys(value) {
				locateMatches(value[key], rest, step(loc, memberStep(key)), out)
			}
		}

	case pathRecursiveMember:
		locateRecursiveMember(current, token.member, rest, loc, out)

	case pathRecursiveWildcard:
		locateRecursiveWildcard(current, rest, loc, out)
	}
}

func locateRecursiveMember(current any, member string, rest []pathToken, loc Location, out *[]located) {
	switch value := current.(type) {
	case map[string]any:
		if child, ok := value[member]; ok {
			locateMatches(child, rest, step(loc, memberStep(member)), out)
		}
		for _, key := range sortedKeys(value) {
			locateRecursiveMember(value[key], member, rest, step(loc, memberStep(key)), out)
		}
	case []any:
		for i, child := range value {
			locateRecursiveMember(child, member, rest, step(loc, indexStep(i)), out)
		}
	}
}

func locateRecursiveWildcard(current any, rest []pathToken, loc Location, out *[]located) {
	switch value := current.(type) {
	case map[string]any:
		for _, key := range sortedKeys(value) {
			child := value[key]
			childLoc := step(loc, memberStep(key))
			locateMatches(child, rest, childLoc, out)
			locateRecursiveWildcard(child, rest, childLoc, out)
		}
	case []any:
		for i, child := range value {
			childLoc := step(loc, indexStep(i))
			locateMatches(child, rest, childLoc, out)
			locateRecursiveWildcard(child, rest, childLoc, out)
		}
	}
}
