package jsonvalue

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type pathTokenKind uint8

const (
	pathMember pathTokenKind = iota
	pathIndex
	pathWildcard
	pathSlice
	pathUnion
	pathFilter
	pathRecursiveMember
	pathRecursiveWildcard
)

type pathToken struct {
	kind       pathTokenKind
	member     string
	index      int
	indices    []int
	sliceStart *int
	sliceEnd   *int
	sliceStep  *int
	filter     *filterExpr
}

type filterExpr struct {
	kind        string
	path        []string
	op          string
	literal     any
	rightPath   []string
	rightIsPath bool
	leftValue   *filterValueExpr
	rightValue  *filterValueExpr
	left        *filterExpr
	right       *filterExpr
}

type filterValueExpr struct {
	kind    string
	path    []string
	literal any
	op      string
	left    *filterValueExpr
	right   *filterValueExpr
	args    []*filterValueExpr
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

	if path[i] == '?' {
		exprStart := i + 1
		j := exprStart
		parenDepth := 0
		bracketDepth := 0
		braceDepth := 0
		inString := byte(0)
		escaped := false

		for j < len(path) {
			ch := path[j]
			if inString != 0 {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == inString {
					inString = 0
				}
				j++
				continue
			}

			if ch == '"' || ch == '\'' {
				inString = ch
				j++
				continue
			}

			switch ch {
			case '(':
				parenDepth++
			case ')':
				if parenDepth == 0 {
					return pathToken{}, 0, errors.New("ERR invalid JSON path")
				}
				parenDepth--
			case '[':
				bracketDepth++
			case ']':
				if bracketDepth > 0 {
					bracketDepth--
				} else if parenDepth == 0 && braceDepth == 0 {
					raw := strings.TrimSpace(path[exprStart:j])
					if raw == "" {
						return pathToken{}, 0, errors.New("ERR invalid JSON path")
					}
					expr, err := parseFilterExpr(raw)
					if err != nil {
						return pathToken{}, 0, err
					}
					return pathToken{kind: pathFilter, filter: expr}, j + 1, nil
				}
			case '{':
				braceDepth++
			case '}':
				if braceDepth == 0 {
					return pathToken{}, 0, errors.New("ERR invalid JSON path")
				}
				braceDepth--
			}
			j++
		}

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

	contentStart := i
	for i < len(path) && path[i] != ']' {
		i++
	}
	if i >= len(path) || path[i] != ']' {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}
	content := path[contentStart:i]
	if content == "" {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}

	if strings.Contains(content, ":") {
		parts := strings.Split(content, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return pathToken{}, 0, errors.New("ERR invalid JSON path")
		}
		parseOptional := func(raw string) (*int, error) {
			if raw == "" {
				return nil, nil
			}
			value, err := strconv.Atoi(raw)
			if err != nil {
				return nil, errors.New("ERR invalid JSON path")
			}
			return &value, nil
		}

		sliceStart, err := parseOptional(parts[0])
		if err != nil {
			return pathToken{}, 0, err
		}
		sliceEnd, err := parseOptional(parts[1])
		if err != nil {
			return pathToken{}, 0, err
		}
		var sliceStep *int
		if len(parts) == 3 {
			sliceStep, err = parseOptional(parts[2])
			if err != nil {
				return pathToken{}, 0, err
			}
			if sliceStep != nil && *sliceStep == 0 {
				return pathToken{}, 0, errors.New("ERR invalid JSON path")
			}
		}
		return pathToken{
			kind:       pathSlice,
			sliceStart: sliceStart,
			sliceEnd:   sliceEnd,
			sliceStep:  sliceStep,
		}, i + 1, nil
	}

	if strings.Contains(content, ",") {
		parts := strings.Split(content, ",")
		indices := make([]int, 0, len(parts))
		for _, part := range parts {
			if part == "" {
				return pathToken{}, 0, errors.New("ERR invalid JSON path")
			}
			index, err := strconv.Atoi(part)
			if err != nil {
				return pathToken{}, 0, errors.New("ERR invalid JSON path")
			}
			indices = append(indices, index)
		}
		return pathToken{kind: pathUnion, indices: indices}, i + 1, nil
	}

	index, err := strconv.Atoi(content)
	if err != nil {
		return pathToken{}, 0, errors.New("ERR invalid JSON path")
	}
	return pathToken{kind: pathIndex, index: index}, i + 1, nil
}

func parseFilterExpr(raw string) (*filterExpr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	for hasOuterParens(raw) {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}

	if index := findTopLevelLogical(raw, "||"); index >= 0 {
		left, err := parseFilterExpr(raw[:index])
		if err != nil {
			return nil, err
		}
		right, err := parseFilterExpr(raw[index+2:])
		if err != nil {
			return nil, err
		}
		return &filterExpr{kind: "or", left: left, right: right}, nil
	}

	if index := findTopLevelLogical(raw, "&&"); index >= 0 {
		left, err := parseFilterExpr(raw[:index])
		if err != nil {
			return nil, err
		}
		right, err := parseFilterExpr(raw[index+2:])
		if err != nil {
			return nil, err
		}
		return &filterExpr{kind: "and", left: left, right: right}, nil
	}

	if strings.HasPrefix(raw, "!") {
		child, err := parseFilterExpr(strings.TrimSpace(raw[1:]))
		if err != nil {
			return nil, err
		}
		return &filterExpr{kind: "not", left: child}, nil
	}

	return parseFilterComparison(raw)
}

func hasOuterParens(raw string) bool {
	if len(raw) < 2 || raw[0] != '(' || raw[len(raw)-1] != ')' {
		return false
	}
	depth := 0
	inString := byte(0)
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(raw)-1 {
				return false
			}
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func findTopLevelLogical(raw, op string) int {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := 0; i <= len(raw)-len(op); i++ {
		ch := raw[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		switch ch {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && strings.HasPrefix(raw[i:], op) {
			return i
		}
	}
	return -1
}

func parseFilterComparison(raw string) (*filterExpr, error) {
	type candidate struct {
		op      string
		isWord  bool
	}
	operators := []candidate{
		{op: "<="}, {op: ">="}, {op: "=="}, {op: "!="}, {op: "=~"},
		{op: "subsetof", isWord: true}, {op: "noneof", isWord: true}, {op: "anyof", isWord: true},
		{op: "sizeof", isWord: true}, {op: "empty", isWord: true},
		{op: "size", isWord: true},
		{op: "nin", isWord: true}, {op: "in", isWord: true},
		{op: "<"}, {op: ">"},
	}

	var op string
	var opIndex int
	inString := byte(0)
	escaped := false
	depth := 0

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		switch ch {
		case '[', '{', '(':
			depth++
			continue
		case ']', '}', ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}

		for _, candidate := range operators {
			if !strings.HasPrefix(raw[i:], candidate.op) {
				continue
			}
			if candidate.isWord {
				beforeOK := i == 0 || isFilterSpace(raw[i-1])
				after := i + len(candidate.op)
				afterOK := after == len(raw) || isFilterSpace(raw[after])
				if !beforeOK || !afterOK {
					continue
				}
			}
			op = candidate.op
			opIndex = i
			break
		}
		if op != "" {
			break
		}
	}
	if op == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	leftRaw := strings.TrimSpace(raw[:opIndex])
	rightRaw := strings.TrimSpace(raw[opIndex+len(op):])
	if leftRaw == "" || rightRaw == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	leftValue, err := parseFilterValueExpr(leftRaw)
	if err != nil {
		return nil, err
	}
	rightValue, err := parseFilterValueExpr(rightRaw)
	if err != nil {
		return nil, err
	}

	return &filterExpr{
		kind:       "compare",
		op:         op,
		leftValue:  leftValue,
		rightValue: rightValue,
	}, nil
}

func parseFilterValueExpr(raw string) (*filterValueExpr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("ERR invalid JSON path")
	}

	for hasOuterParens(raw) {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}

	for _, name := range []string{"first", "last"} {
		prefix := name + "("
		if strings.HasPrefix(raw, prefix) && strings.HasSuffix(raw, ")") {
			inner := strings.TrimSpace(raw[len(prefix) : len(raw)-1])
			arg, err := parseFilterValueExpr(inner)
			if err != nil {
				return nil, err
			}
			return &filterValueExpr{kind: "func", op: name, left: arg}, nil
		}

		suffix := "." + name + "()"
		if strings.HasSuffix(raw, suffix) {
			base := strings.TrimSpace(raw[:len(raw)-len(suffix)])
			if base == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			arg, err := parseFilterValueExpr(base)
			if err != nil {
				return nil, err
			}
			return &filterValueExpr{kind: "func", op: name, left: arg}, nil
		}
	}

	if strings.HasPrefix(raw, "append(") && strings.HasSuffix(raw, ")") {
		parts, err := splitFunctionArgs(raw[len("append(") : len(raw)-1])
		if err != nil || len(parts) < 2 {
			return nil, errors.New("ERR invalid JSON path")
		}
		base, err := parseFilterValueExpr(parts[0])
		if err != nil {
			return nil, err
		}
		args := make([]*filterValueExpr, 0, len(parts)-1)
		for _, part := range parts[1:] {
			arg, err := parseFilterValueExpr(part)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}
		return &filterValueExpr{kind: "func", op: "append", left: base, args: args}, nil
	}

	if strings.Contains(raw, ".append(") && strings.HasSuffix(raw, ")") {
		call := strings.LastIndex(raw, ".append(")
		if call > 0 {
			baseRaw := strings.TrimSpace(raw[:call])
			argsRaw := strings.TrimSpace(raw[call+len(".append(") : len(raw)-1])
			if baseRaw == "" || argsRaw == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			base, err := parseFilterValueExpr(baseRaw)
			if err != nil {
				return nil, err
			}
			parts, err := splitFunctionArgs(argsRaw)
			if err != nil || len(parts) == 0 {
				return nil, errors.New("ERR invalid JSON path")
			}
			args := make([]*filterValueExpr, 0, len(parts))
			for _, part := range parts {
				arg, err := parseFilterValueExpr(part)
				if err != nil {
					return nil, err
				}
				args = append(args, arg)
			}
			return &filterValueExpr{kind: "func", op: "append", left: base, args: args}, nil
		}
	}

	if strings.HasPrefix(raw, "index(") && strings.HasSuffix(raw, ")") {
		args, err := splitFunctionArgs(raw[len("index(") : len(raw)-1])
		if err != nil || len(args) != 2 {
			return nil, errors.New("ERR invalid JSON path")
		}
		arrayExpr, err := parseFilterValueExpr(args[0])
		if err != nil {
			return nil, err
		}
		indexExpr, err := parseFilterValueExpr(args[1])
		if err != nil {
			return nil, err
		}
		return &filterValueExpr{kind: "func", op: "index", left: arrayExpr, right: indexExpr}, nil
	}

	if strings.Contains(raw, ".index(") && strings.HasSuffix(raw, ")") {
		call := strings.LastIndex(raw, ".index(")
		if call > 0 {
			base := strings.TrimSpace(raw[:call])
			argRaw := strings.TrimSpace(raw[call+len(".index(") : len(raw)-1])
			if base == "" || argRaw == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			arrayExpr, err := parseFilterValueExpr(base)
			if err != nil {
				return nil, err
			}
			indexExpr, err := parseFilterValueExpr(argRaw)
			if err != nil {
				return nil, err
			}
			return &filterValueExpr{kind: "func", op: "index", left: arrayExpr, right: indexExpr}, nil
		}
	}

	for _, name := range []string{"length", "abs", "ceiling", "floor", "min", "max", "sum", "avg", "stddev"} {
		prefix := name + "("
		if strings.HasPrefix(raw, prefix) && strings.HasSuffix(raw, ")") {
			inner := strings.TrimSpace(raw[len(prefix) : len(raw)-1])
			arg, err := parseFilterValueExpr(inner)
			if err != nil {
				return nil, err
			}
			return &filterValueExpr{kind: "func", op: name, left: arg}, nil
		}

		suffix := "." + name + "()"
		if strings.HasSuffix(raw, suffix) {
			base := strings.TrimSpace(raw[:len(raw)-len(suffix)])
			if base == "" {
				return nil, errors.New("ERR invalid JSON path")
			}
			arg, err := parseFilterValueExpr(base)
			if err != nil {
				return nil, err
			}
			return &filterValueExpr{kind: "func", op: name, left: arg}, nil
		}
	}

	if index, op := findTopLevelArithmetic(raw, []string{" + ", " - "}); index >= 0 {
		left, err := parseFilterValueExpr(raw[:index])
		if err != nil {
			return nil, err
		}
		right, err := parseFilterValueExpr(raw[index+len(op):])
		if err != nil {
			return nil, err
		}
		return &filterValueExpr{kind: "binary", op: strings.TrimSpace(op), left: left, right: right}, nil
	}

	if index, op := findTopLevelArithmetic(raw, []string{" * ", " / ", " % "}); index >= 0 {
		left, err := parseFilterValueExpr(raw[:index])
		if err != nil {
			return nil, err
		}
		right, err := parseFilterValueExpr(raw[index+len(op):])
		if err != nil {
			return nil, err
		}
		return &filterValueExpr{kind: "binary", op: strings.TrimSpace(op), left: left, right: right}, nil
	}

	if strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-") {
		op := raw[:1]
		child, err := parseFilterValueExpr(strings.TrimSpace(raw[1:]))
		if err != nil {
			return nil, err
		}
		return &filterValueExpr{kind: "unary", op: op, left: child}, nil
	}

	if raw == "@" || strings.HasPrefix(raw, "@.") {
		path, err := parseFilterPath(raw)
		if err != nil {
			return nil, err
		}
		return &filterValueExpr{kind: "path", path: path}, nil
	}

	var literal any
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		literal = strings.ReplaceAll(raw[1:len(raw)-1], "\\'", "'")
		return &filterValueExpr{kind: "literal", literal: literal}, nil
	}
	if err := json.Unmarshal([]byte(raw), &literal); err != nil {
		return nil, errors.New("ERR invalid JSON path")
	}
	return &filterValueExpr{kind: "literal", literal: literal}, nil
}

func findTopLevelArithmetic(raw string, operators []string) (int, string) {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := len(raw) - 1; i >= 0; i-- {
		ch := raw[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		switch ch {
		case ')', ']', '}':
			depth++
			continue
		case '(', '[', '{':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}
		for _, op := range operators {
			start := i - len(op) + 1
			if start >= 0 && raw[start:i+1] == op {
				return start, op
			}
		}
	}
	return -1, ""
}

func evalFilterValue(current any, expr *filterValueExpr) (any, bool) {
	if expr == nil {
		return nil, false
	}
	switch expr.kind {
	case "path":
		return filterValue(current, expr.path)
	case "literal":
		return expr.literal, true
	case "func":
		value, ok := evalFilterValue(current, expr.left)
		if !ok {
			return nil, false
		}
		switch expr.op {
		case "length":
			size, ok := filterSize(value)
			if !ok {
				return nil, false
			}
			return float64(size), true

		case "append":
			array, ok := value.([]any)
			if !ok {
				return nil, false
			}
			projected := append([]any(nil), array...)
			for _, argExpr := range expr.args {
				arg, ok := evalFilterValue(current, argExpr)
				if !ok {
					return nil, false
				}
				projected = append(projected, arg)
			}
			return projected, true

		case "first", "last":
			array, ok := value.([]any)
			if !ok || len(array) == 0 {
				return nil, false
			}
			if expr.op == "first" {
				return array[0], true
			}
			return array[len(array)-1], true

		case "index":
			array, ok := value.([]any)
			if !ok || expr.right == nil {
				return nil, false
			}
			rawIndex, ok := evalFilterValue(current, expr.right)
			if !ok {
				return nil, false
			}
			number, ok := rawIndex.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, false
			}
			index := int(math.Trunc(number))
			if index < 0 {
				index += len(array)
			}
			if index < 0 || index >= len(array) {
				return nil, false
			}
			return array[index], true

		case "min", "max", "sum", "avg", "stddev":
			array, ok := value.([]any)
			if !ok || len(array) == 0 {
				return nil, false
			}

			numbers := make([]float64, len(array))
			for i, item := range array {
				number, ok := item.(float64)
				if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
					return nil, false
				}
				numbers[i] = number
			}

			switch expr.op {
			case "min":
				result := numbers[0]
				for _, number := range numbers[1:] {
					if number < result {
						result = number
					}
				}
				return result, true
			case "max":
				result := numbers[0]
				for _, number := range numbers[1:] {
					if number > result {
						result = number
					}
				}
				return result, true
			case "sum":
				total := 0.0
				for _, number := range numbers {
					total += number
				}
				if math.IsNaN(total) || math.IsInf(total, 0) {
					return nil, false
				}
				return total, true
			case "avg":
				total := 0.0
				for _, number := range numbers {
					total += number
				}
				result := total / float64(len(numbers))
				if math.IsNaN(result) || math.IsInf(result, 0) {
					return nil, false
				}
				return result, true
			case "stddev":
				total := 0.0
				for _, number := range numbers {
					total += number
				}
				mean := total / float64(len(numbers))
				variance := 0.0
				for _, number := range numbers {
					delta := number - mean
					variance += delta * delta
				}
				result := math.Sqrt(variance / float64(len(numbers)))
				if math.IsNaN(result) || math.IsInf(result, 0) {
					return nil, false
				}
				return result, true
			}
			return nil, false

		case "abs", "ceiling", "floor":
			number, ok := value.(float64)
			if !ok {
				return nil, false
			}

			result := number
			switch expr.op {
			case "abs":
				result = math.Abs(number)
			case "ceiling":
				result = math.Ceil(number)
			case "floor":
				result = math.Floor(number)
			}

			if math.IsNaN(result) || math.IsInf(result, 0) {
				return nil, false
			}
			// JSON numeric values currently use float64 internally. Reject
			// integral results that cannot be represented as signed int64,
			// matching Redis' overflow-to-Nothing rule for these functions.
			if result == math.Trunc(result) && (result < math.MinInt64 || result > math.MaxInt64) {
				return nil, false
			}
			return result, true

		default:
			return nil, false
		}
	case "unary":
		value, ok := evalFilterValue(current, expr.left)
		if !ok {
			return nil, false
		}
		number, ok := value.(float64)
		if !ok {
			return nil, false
		}
		if expr.op == "-" {
			number = -number
		}
		return number, true
	case "binary":
		leftValue, leftOK := evalFilterValue(current, expr.left)
		rightValue, rightOK := evalFilterValue(current, expr.right)
		if !leftOK || !rightOK {
			return nil, false
		}
		leftNumber, leftOK := leftValue.(float64)
		rightNumber, rightOK := rightValue.(float64)
		if !leftOK || !rightOK {
			return nil, false
		}
		var result float64
		switch expr.op {
		case "+":
			result = leftNumber + rightNumber
		case "-":
			result = leftNumber - rightNumber
		case "*":
			result = leftNumber * rightNumber
		case "/":
			if rightNumber == 0 {
				return nil, false
			}
			result = leftNumber / rightNumber
		case "%":
			if rightNumber == 0 {
				return nil, false
			}
			result = math.Mod(leftNumber, rightNumber)
		default:
			return nil, false
		}
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return nil, false
		}
		return result, true
	default:
		return nil, false
	}
}

func splitFunctionArgs(raw string) ([]string, error) {
	args := make([]string, 0, 2)
	start := 0
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	inString := byte(0)
	escaped := false

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		switch ch {
		case '(':
			parenDepth++
		case ')':
			if parenDepth == 0 {
				return nil, errors.New("ERR invalid JSON path")
			}
			parenDepth--
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth == 0 {
				return nil, errors.New("ERR invalid JSON path")
			}
			bracketDepth--
		case '{':
			braceDepth++
		case '}':
			if braceDepth == 0 {
				return nil, errors.New("ERR invalid JSON path")
			}
			braceDepth--
		case ',':
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				arg := strings.TrimSpace(raw[start:i])
				if arg == "" {
					return nil, errors.New("ERR invalid JSON path")
				}
				args = append(args, arg)
				start = i + 1
			}
		}
	}
	if inString != 0 || parenDepth != 0 || bracketDepth != 0 || braceDepth != 0 {
		return nil, errors.New("ERR invalid JSON path")
	}
	arg := strings.TrimSpace(raw[start:])
	if arg == "" {
		return nil, errors.New("ERR invalid JSON path")
	}
	args = append(args, arg)
	return args, nil
}

func parseFilterPath(raw string) ([]string, error) {
	if raw == "@" {
		return nil, nil
	}
	if !strings.HasPrefix(raw, "@.") {
		return nil, errors.New("ERR invalid JSON path")
	}
	parts := strings.Split(strings.TrimPrefix(raw, "@."), ".")
	for _, part := range parts {
		if part == "" {
			return nil, errors.New("ERR invalid JSON path")
		}
	}
	return parts, nil
}

func isFilterSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}


func filterValue(current any, fields []string) (any, bool) {
	value := current
	for _, field := range fields {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = object[field]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func matchesFilter(current any, expr *filterExpr) bool {
	if expr == nil {
		return false
	}

	switch expr.kind {
	case "or":
		return matchesFilter(current, expr.left) || matchesFilter(current, expr.right)
	case "and":
		return matchesFilter(current, expr.left) && matchesFilter(current, expr.right)
	case "not":
		return !matchesFilter(current, expr.left)
	case "compare":
	default:
		return false
	}

	var leftValue any
	var rightValue any
	var ok bool

	if expr.leftValue != nil {
		leftValue, ok = evalFilterValue(current, expr.leftValue)
		if !ok {
			return false
		}
		rightValue, ok = evalFilterValue(current, expr.rightValue)
		if !ok {
			return false
		}
	} else {
		// Backward-compatible fallback for expressions built by older code.
		leftValue, ok = filterValue(current, expr.path)
		if !ok {
			return false
		}
		rightValue = expr.literal
		if expr.rightIsPath {
			rightValue, ok = filterValue(current, expr.rightPath)
			if !ok {
				return false
			}
		}
	}

	switch expr.op {
	case "==":
		return filterEqual(leftValue, rightValue)
	case "!=":
		return !filterEqual(leftValue, rightValue)
	case "=~":
		leftString, ok := leftValue.(string)
		if !ok {
			return false
		}
		pattern, ok := rightValue.(string)
		if !ok {
			return false
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(leftString)
	case "in", "nin":
		array, ok := rightValue.([]any)
		match := false
		if ok {
			for _, candidate := range array {
				if filterEqual(leftValue, candidate) {
					match = true
					break
				}
			}
		}
		if expr.op == "nin" {
			return !match
		}
		return match

	case "subsetof", "anyof", "noneof":
		leftArray, leftOK := leftValue.([]any)
		rightArray, rightOK := rightValue.([]any)
		if !leftOK || !rightOK {
			return false
		}
		switch expr.op {
		case "subsetof":
			for _, value := range leftArray {
				if !arrayContains(rightArray, value) {
					return false
				}
			}
			return true
		case "anyof":
			for _, value := range leftArray {
				if arrayContains(rightArray, value) {
					return true
				}
			}
			return false
		case "noneof":
			for _, value := range leftArray {
				if arrayContains(rightArray, value) {
					return false
				}
			}
			return true
		}

	case "size", "sizeof":
		size, ok := filterSize(leftValue)
		if !ok {
			return false
		}
		number, ok := rightValue.(float64)
		if !ok {
			return false
		}
		return size == int(number)

	case "empty":
		wantEmpty, ok := rightValue.(bool)
		if !ok {
			return false
		}
		size, ok := filterSize(leftValue)
		if !ok {
			return false
		}
		return (size == 0) == wantEmpty
	}

	switch left := leftValue.(type) {
	case float64:
		right, ok := rightValue.(float64)
		if !ok {
			return false
		}
		switch expr.op {
		case "<":
			return left < right
		case "<=":
			return left <= right
		case ">":
			return left > right
		case ">=":
			return left >= right
		}
	case string:
		right, ok := rightValue.(string)
		if !ok {
			return false
		}
		switch expr.op {
		case "<":
			return left < right
		case "<=":
			return left <= right
		case ">":
			return left > right
		case ">=":
			return left >= right
		}
	}
	return false
}

func filterEqual(left, right any) bool {
	// JSON numbers are decoded as float64 today, so this already gives the
	// desired numeric value equality for SnugKV's current representation.
	return reflect.DeepEqual(left, right)
}

func arrayContains(values []any, candidate any) bool {
	for _, value := range values {
		if filterEqual(value, candidate) {
			return true
		}
	}
	return false
}

func filterSize(value any) (int, bool) {
	switch typed := value.(type) {
	case string:
		// Redis defines size in characters, not bytes.
		return len([]rune(typed)), true
	case []any:
		return len(typed), true
	case map[string]any:
		return len(typed), true
	default:
		return 0, false
	}
}

func filteredArrayIndices(array []any, expr *filterExpr) []int {
	out := make([]int, 0)
	for i, value := range array {
		if matchesFilter(value, expr) {
			out = append(out, i)
		}
	}
	return out
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

func sliceIndices(length int, start, end, step *int) []int {
	stride := 1
	if step != nil {
		stride = *step
	}

	if stride > 0 {
		first := 0
		last := length
		if start != nil {
			first = *start
			if first < 0 {
				first += length
			}
		}
		if end != nil {
			last = *end
			if last < 0 {
				last += length
			}
		}
		if first < 0 {
			first = 0
		}
		if first > length {
			first = length
		}
		if last < 0 {
			last = 0
		}
		if last > length {
			last = length
		}
		out := make([]int, 0)
		for i := first; i < last; i += stride {
			out = append(out, i)
		}
		return out
	}

	first := length - 1
	last := -1
	if start != nil {
		first = *start
		if first < 0 {
			first += length
		}
	}
	if end != nil {
		last = *end
		if last < 0 {
			last += length
		}
	}
	if first >= length {
		first = length - 1
	}
	if first < -1 {
		first = -1
	}
	if last >= length {
		last = length - 1
	}
	if last < -1 {
		last = -1
	}
	out := make([]int, 0)
	for i := first; i > last; i += stride {
		if i >= 0 && i < length {
			out = append(out, i)
		}
	}
	return out
}

func unionIndices(length int, raw []int) []int {
	out := make([]int, 0, len(raw))
	seen := make(map[int]struct{}, len(raw))
	for _, index := range raw {
		resolved, ok := resolveIndex(length, index)
		if !ok {
			continue
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	return out
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

	case pathSlice:
		array, ok := current.([]any)
		if !ok {
			return
		}
		for _, index := range sliceIndices(len(array), token.sliceStart, token.sliceEnd, token.sliceStep) {
			collectMatches(array[index], rest, out)
		}

	case pathUnion:
		array, ok := current.([]any)
		if !ok {
			return
		}
		for _, index := range unionIndices(len(array), token.indices) {
			collectMatches(array[index], rest, out)
		}

	case pathFilter:
		switch container := current.(type) {
		case []any:
			for _, index := range filteredArrayIndices(container, token.filter) {
				collectMatches(container[index], rest, out)
			}
		case map[string]any:
			keys := make([]string, 0, len(container))
			for key := range container {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if matchesFilter(container[key], token.filter) {
					collectMatches(container[key], rest, out)
				}
			}
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
		// Recursive descent uses preorder semantics: a matching member on the
		// current object is emitted before matches found deeper below it.
		if child, ok := value[member]; ok {
			collectMatches(child, rest, out)
		}

		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectRecursiveMember(value[key], member, rest, out)
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
	rest := tokens[1:]
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

	case pathSlice:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		indices := sliceIndices(len(array), token.sliceStart, token.sliceEnd, token.sliceStep)
		count := 0
		for _, index := range indices {
			if last {
				array[index] = value
				count++
				continue
			}
			updated, n := setMatchesAt(array[index], rest, value)
			if n > 0 {
				array[index] = updated
				count += n
			}
		}
		return array, count

	case pathUnion:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		count := 0
		for _, index := range unionIndices(len(array), token.indices) {
			if last {
				array[index] = value
				count++
				continue
			}
			updated, n := setMatchesAt(array[index], rest, value)
			if n > 0 {
				array[index] = updated
				count += n
			}
		}
		return array, count

	case pathFilter:
		switch container := current.(type) {
		case []any:
			count := 0
			for _, index := range filteredArrayIndices(container, token.filter) {
				if last {
					container[index] = value
					count++
					continue
				}
				updated, n := setMatchesAt(container[index], rest, value)
				if n > 0 {
					container[index] = updated
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
			for _, key := range keys {
				child := container[key]
				if !matchesFilter(child, token.filter) {
					continue
				}
				if last {
					container[key] = value
					count++
					continue
				}
				updated, n := setMatchesAt(child, rest, value)
				if n > 0 {
					container[key] = updated
					count += n
				}
			}
			return container, count
		}

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
	rest := tokens[1:]
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

	case pathSlice:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		indices := sliceIndices(len(array), token.sliceStart, token.sliceEnd, token.sliceStep)
		if last {
			sort.Sort(sort.Reverse(sort.IntSlice(indices)))
			for _, index := range indices {
				array = append(array[:index], array[index+1:]...)
			}
			return array, len(indices)
		}
		count := 0
		for _, index := range indices {
			updated, n := deleteMatchesAt(array[index], rest)
			if n > 0 {
				array[index] = updated
				count += n
			}
		}
		return array, count

	case pathUnion:
		array, ok := current.([]any)
		if !ok {
			return current, 0
		}
		indices := unionIndices(len(array), token.indices)
		if last {
			sort.Sort(sort.Reverse(sort.IntSlice(indices)))
			for _, index := range indices {
				array = append(array[:index], array[index+1:]...)
			}
			return array, len(indices)
		}
		count := 0
		for _, index := range indices {
			updated, n := deleteMatchesAt(array[index], rest)
			if n > 0 {
				array[index] = updated
				count += n
			}
		}
		return array, count

	case pathFilter:
		switch container := current.(type) {
		case []any:
			indices := filteredArrayIndices(container, token.filter)
			if last {
				sort.Sort(sort.Reverse(sort.IntSlice(indices)))
				for _, index := range indices {
					container = append(container[:index], container[index+1:]...)
				}
				return container, len(indices)
			}
			count := 0
			for _, index := range indices {
				updated, n := deleteMatchesAt(container[index], rest)
				if n > 0 {
					container[index] = updated
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
			for _, key := range keys {
				child := container[key]
				if !matchesFilter(child, token.filter) {
					continue
				}
				if last {
					delete(container, key)
					count++
					continue
				}
				updated, n := deleteMatchesAt(child, rest)
				if n > 0 {
					container[key] = updated
					count += n
				}
			}
			return container, count
		}

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
