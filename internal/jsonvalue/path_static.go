package jsonvalue

// IsStaticPath reports whether path identifies one deterministic location.
// Redis allows JSON.SET to create a missing terminal member only for static
// member/index paths; selectors such as filters, wildcards, slices, unions and
// recursive descent cannot be used to create a previously missing location.
func IsStaticPath(path string) (bool, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return false, err
	}
	for _, token := range tokens {
		switch token.kind {
		case pathMember, pathIndex:
		default:
			return false, nil
		}
	}
	return true, nil
}
