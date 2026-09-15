package engine

import "testing"

func TestRedisGlobMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern []byte
		value   []byte
		want    bool
	}{
		{"empty-empty", []byte(""), []byte(""), true},
		{"empty-nonempty", []byte(""), []byte("x"), false},
		{"star", []byte("h*llo"), []byte("heeeello"), true},
		{"star-empty", []byte("h*llo"), []byte("hllo"), true},
		{"question", []byte("h?llo"), []byte("hello"), true},
		{"question-width", []byte("?"), []byte{0xff}, true},
		{"class", []byte("h[ae]llo"), []byte("hallo"), true},
		{"class-miss", []byte("h[ae]llo"), []byte("hillo"), false},
		{"range", []byte("h[a-c]llo"), []byte("hbllo"), true},
		{"negated-caret", []byte("h[^e]llo"), []byte("hallo"), true},
		{"negated-caret-miss", []byte("h[^e]llo"), []byte("hello"), false},
		{"negated-bang", []byte("h[!e]llo"), []byte("hallo"), true},
		{"escaped-star", []byte(`a\*b`), []byte("a*b"), true},
		{"escaped-question", []byte(`a\?b`), []byte("a?b"), true},
		{"escaped-bracket", []byte(`a\[b`), []byte("a[b"), true},
		{"literal-trailing-backslash", []byte{'a', '\\'}, []byte{'a', '\\'}, true},
		{"unterminated-class-literal", []byte("a[b"), []byte("a[b"), true},
		{"binary-nul", []byte{'a', '*', 0x00, '?'}, []byte{'a', 'x', 0x00, 0xff}, true},
		{"collapsed-stars", []byte("a***b"), []byte("ab"), true},
		{"must-consume-all", []byte("a*"), []byte("ba"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redisGlobMatch(tc.pattern, tc.value); got != tc.want {
				t.Fatalf("redisGlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}
