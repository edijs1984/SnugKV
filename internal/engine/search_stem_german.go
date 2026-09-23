package engine

import "strings"

// stemSearchGerman implements the Snowball-style normalization needed by the
// audited Redis Search German stemming surface. Input must already be lowercase.
func stemSearchGerman(word string) string {
	if word == "" {
		return word
	}

	w := strings.NewReplacer(
		"ä", "a",
		"ö", "o",
		"ü", "u",
		"ß", "ss",
	).Replace(word)

	// Remove common inflectional endings. Longest suffixes must be checked first.
	for _, suffix := range []string{"ern", "em", "er", "en", "es", "e", "s"} {
		if !strings.HasSuffix(w, suffix) {
			continue
		}
		base := strings.TrimSuffix(w, suffix)
		if len([]rune(base)) < 3 {
			continue
		}
		return base
	}
	return w
}
