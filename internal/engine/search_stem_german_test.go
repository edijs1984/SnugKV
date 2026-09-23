package engine

import "testing"

func TestStemSearchGermanFamilies(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"haus", "haus"},
		{"hauses", "haus"},
		{"häuser", "haus"},
		{"häusern", "haus"},
	} {
		if got := stemSearchGerman(tc.in); got != tc.want {
			t.Fatalf("stemSearchGerman(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}
