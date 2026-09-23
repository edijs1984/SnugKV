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
	for _, suffix := range []string{"ern", "em", "er", "en", "es", "e"} {
		if !strings.HasSuffix(w, suffix) {
			continue
		}
		base := strings.TrimSuffix(w, suffix)
		if len([]rune(base)) < 3 {
			continue
		}
		return base
	}

	// German Snowball removes a final 's' only after a restricted set of
	// preceding letters. In particular, "haus" must remain "haus".
	if strings.HasSuffix(w, "s") && len(w) > 1 {
		prev := w[len(w)-2]
		if strings.ContainsRune("bdfghklmnrt", rune(prev)) {
			base := w[:len(w)-1]
			if len([]rune(base)) >= 3 {
				return base
			}
		}
	}
	return w
}
