package secrets

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ParseDotenv reads KEY=VALUE lines as written in .env files: blank lines and
// # comments are skipped, an "export " prefix is allowed, values may be
// unquoted (a " #" starts a comment), 'single quoted' (literal) or "double
// quoted" (with \n, \t, \", \\ escapes, and may span lines).
func ParseDotenv(text string) (map[string]string, error) {
	out := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", i+1)
		}
		key := strings.TrimSpace(line[:eq])
		if !nameRe.MatchString(key) {
			return nil, fmt.Errorf("line %d: %q is not a valid variable name", i+1, key)
		}
		raw := strings.TrimLeft(line[eq+1:], " \t")
		switch {
		case strings.HasPrefix(raw, `"`):
			body := raw[1:]
			start := i
			for {
				if end := closingQuote(body); end >= 0 {
					val, err := unescape(body[:end])
					if err != nil {
						return nil, fmt.Errorf("line %d: %v", start+1, err)
					}
					out[key] = val
					break
				}
				i++
				if i >= len(lines) {
					return nil, fmt.Errorf("line %d: unterminated double quote", start+1)
				}
				body += "\n" + lines[i]
			}
		case strings.HasPrefix(raw, "'"):
			end := strings.IndexByte(raw[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated single quote", i+1)
			}
			out[key] = raw[1 : end+1]
		default:
			if c := strings.Index(raw, " #"); c >= 0 {
				raw = raw[:c]
			}
			out[key] = strings.TrimSpace(raw)
		}
	}
	return out, nil
}

func closingQuote(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func unescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", errors.New("trailing backslash")
		}
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '"', '\\', '$':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

// FormatDotenv writes values as a .env file that ParseDotenv reads back
// unchanged, sorted by name.
func FormatDotenv(vals map[string]string) string {
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quoteValue(vals[k]))
		b.WriteByte('\n')
	}
	return b.String()
}

func quoteValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\r\n\"'\\#$=") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "$", `\$`)
	return `"` + r.Replace(v) + `"`
}

func sortStrings(s []string) { sort.Strings(s) }
