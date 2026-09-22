package engine

import "strings"

// stemSearchEnglish implements a compact English Snowball/Porter2-style
// stemmer for SnugKV TEXT search. Input must already be lowercase.
func stemSearchEnglish(word string) string {
	if len(word) <= 2 {
		return word
	}

	switch word {
	case "skis":
		return "ski"
	case "skies":
		return "sky"
	case "dying":
		return "die"
	case "lying":
		return "lie"
	case "tying":
		return "tie"
	case "idly":
		return "idl"
	case "gently":
		return "gentl"
	case "ugly":
		return "ugli"
	case "early":
		return "earli"
	case "only":
		return "onli"
	case "singly":
		return "singl"
	case "sky", "news", "howe", "atlas", "cosmos", "bias", "andes":
		return word
	}

	w := strings.TrimLeft(word, "'")
	w = step0English(w)

	r1, r2 := englishRegions(w)
	w = step1aEnglish(w)
	w = step1bEnglish(w, r1)
	w = step1cEnglish(w)
	w = step2English(w, r1)
	w = step3English(w, r1, r2)
	w = step4English(w, r2)
	w = step5English(w, r1, r2)
	return w
}

func englishVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	default:
		return false
	}
}

func englishRegions(w string) (int, int) {
	r1 := len(w)
	for i := 1; i < len(w); i++ {
		if englishVowel(w[i-1]) && !englishVowel(w[i]) {
			r1 = i + 1
			break
		}
	}
	switch {
	case strings.HasPrefix(w, "gener"):
		r1 = 5
	case strings.HasPrefix(w, "commun"):
		r1 = 6
	case strings.HasPrefix(w, "arsen"):
		r1 = 5
	}

	r2 := len(w)
	for i := r1 + 1; i < len(w); i++ {
		if englishVowel(w[i-1]) && !englishVowel(w[i]) {
			r2 = i + 1
			break
		}
	}
	return r1, r2
}

func hasEnglishVowel(w string) bool {
	for i := 0; i < len(w); i++ {
		if englishVowel(w[i]) {
			return true
		}
	}
	return false
}

func englishShortSyllable(w string) bool {
	n := len(w)
	if n >= 3 {
		a, b, c := w[n-3], w[n-2], w[n-1]
		if !englishVowel(a) && englishVowel(b) && !englishVowel(c) &&
			c != 'w' && c != 'x' && c != 'y' {
			return true
		}
	}
	if n == 2 && englishVowel(w[0]) && !englishVowel(w[1]) {
		return true
	}
	return false
}

func step0English(w string) string {
	for _, suffix := range []string{"'s'", "'s", "'"} {
		if strings.HasSuffix(w, suffix) {
			return strings.TrimSuffix(w, suffix)
		}
	}
	return w
}

func step1aEnglish(w string) string {
	switch {
	case strings.HasSuffix(w, "sses"):
		return strings.TrimSuffix(w, "sses") + "ss"
	case strings.HasSuffix(w, "ied"), strings.HasSuffix(w, "ies"):
		base := w[:len(w)-3]
		if len(base) > 1 {
			return base + "i"
		}
		return base + "ie"
	case strings.HasSuffix(w, "us"), strings.HasSuffix(w, "ss"):
		return w
	case strings.HasSuffix(w, "s"):
		base := w[:len(w)-1]
		if len(base) >= 2 && hasEnglishVowel(base[:len(base)-1]) {
			return base
		}
	}
	return w
}

func step1bEnglish(w string, r1 int) string {
	for _, suffix := range []string{"eedly", "eed"} {
		if strings.HasSuffix(w, suffix) {
			start := len(w) - len(suffix)
			if start >= r1 {
				return w[:start] + "ee"
			}
			return w
		}
	}

	for _, suffix := range []string{"ingly", "edly", "ing", "ed"} {
		if !strings.HasSuffix(w, suffix) {
			continue
		}
		base := w[:len(w)-len(suffix)]
		if !hasEnglishVowel(base) {
			return w
		}
		w = base
		switch {
		case strings.HasSuffix(w, "at"), strings.HasSuffix(w, "bl"), strings.HasSuffix(w, "iz"):
			return w + "e"
		case len(w) >= 2:
			last2 := w[len(w)-2:]
			switch last2 {
			case "bb", "dd", "ff", "gg", "mm", "nn", "pp", "rr", "tt":
				return w[:len(w)-1]
			}
		}
		r1Now, _ := englishRegions(w)
		if r1Now >= len(w) && englishShortSyllable(w) {
			return w + "e"
		}
		return w
	}
	return w
}

func step1cEnglish(w string) string {
	if len(w) > 2 {
		last := w[len(w)-1]
		if (last == 'y' || last == 'Y') && !englishVowel(w[len(w)-2]) {
			return w[:len(w)-1] + "i"
		}
	}
	return w
}

func replaceEnglishInRegion(w, suffix, replacement string, region int) (string, bool) {
	if !strings.HasSuffix(w, suffix) {
		return w, false
	}
	start := len(w) - len(suffix)
	if start < region {
		return w, true
	}
	return w[:start] + replacement, true
}

func step2English(w string, r1 int) string {
	rules := []struct {
		suffix string
		repl   string
	}{
		{"ization", "ize"}, {"ational", "ate"}, {"fulness", "ful"}, {"ousness", "ous"},
		{"iveness", "ive"}, {"tional", "tion"}, {"biliti", "ble"}, {"lessli", "less"},
		{"entli", "ent"}, {"ation", "ate"}, {"alism", "al"}, {"aliti", "al"},
		{"ousli", "ous"}, {"iviti", "ive"}, {"fulli", "ful"}, {"enci", "ence"},
		{"anci", "ance"}, {"abli", "able"}, {"izer", "ize"}, {"ator", "ate"},
		{"alli", "al"}, {"bli", "ble"},
	}
	for _, rule := range rules {
		if out, matched := replaceEnglishInRegion(w, rule.suffix, rule.repl, r1); matched {
			return out
		}
	}

	if strings.HasSuffix(w, "ogi") {
		start := len(w) - 3
		if start >= r1 && start > 0 && w[start-1] == 'l' {
			return w[:start] + "og"
		}
		return w
	}
	if strings.HasSuffix(w, "li") {
		start := len(w) - 2
		if start >= r1 && start > 0 && strings.ContainsRune("cdeghkmnrt", rune(w[start-1])) {
			return w[:start]
		}
	}
	return w
}

func step3English(w string, r1, r2 int) string {
	rules := []struct {
		suffix string
		repl   string
	}{
		{"ational", "ate"}, {"tional", "tion"}, {"alize", "al"}, {"icate", "ic"},
		{"iciti", "ic"}, {"ical", "ic"}, {"ful", ""}, {"ness", ""},
	}
	for _, rule := range rules {
		if out, matched := replaceEnglishInRegion(w, rule.suffix, rule.repl, r1); matched {
			return out
		}
	}
	if strings.HasSuffix(w, "ative") {
		start := len(w) - len("ative")
		if start >= r2 {
			return w[:start]
		}
	}
	return w
}

func step4English(w string, r2 int) string {
	for _, suffix := range []string{
		"ement", "ance", "ence", "able", "ible", "ment", "ant", "ent",
		"ism", "ate", "iti", "ous", "ive", "ize", "al", "er", "ic",
	} {
		if strings.HasSuffix(w, suffix) {
			start := len(w) - len(suffix)
			if start >= r2 {
				return w[:start]
			}
			return w
		}
	}

	if strings.HasSuffix(w, "ion") {
		start := len(w) - 3
		if start >= r2 && start > 0 {
			prev := w[start-1]
			if prev == 's' || prev == 't' {
				return w[:start]
			}
		}
	}
	return w
}

func step5English(w string, r1, r2 int) string {
	if strings.HasSuffix(w, "e") {
		start := len(w) - 1
		if start >= r2 {
			return w[:start]
		}
		if start >= r1 && !englishShortSyllable(w[:start]) {
			return w[:start]
		}
		return w
	}
	if strings.HasSuffix(w, "ll") && len(w)-1 >= r2 {
		return w[:len(w)-1]
	}
	return w
}
