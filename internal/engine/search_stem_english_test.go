package engine

import "testing"

func TestStemSearchEnglishFamilies(t *testing.T) {
	tests := map[string]string{
		"hire":    "hire",
		"hired":   "hire",
		"hiring":  "hire",
		"study":   "studi",
		"studies": "studi",
		"studied": "studi",
		"run":     "run",
		"runs":    "run",
		"running": "run",
		"search":  "search",
		"searched":"search",
		"searching":"search",
	}
	for input, want := range tests {
		if got := stemSearchEnglish(input); got != want {
			t.Fatalf("stemSearchEnglish(%q)=%q want=%q", input, got, want)
		}
	}
}
