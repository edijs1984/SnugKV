package server

import "testing"

func TestConcurrentScalarCommandIncludesSimpleNativeWrites(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want bool
	}{
		{"hset-simple", [][]byte{[]byte("HSET"), []byte("h"), []byte("f"), []byte("v")}, true},
		{"rpush-simple", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")}, true},
		{"sadd-simple", [][]byte{[]byte("SADD"), []byte("s"), []byte("m")}, true},
		{"zadd-simple", [][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")}, true},
		{"hset-multi", [][]byte{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")}, false},
		{"rpush-multi", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("a"), []byte("b")}, false},
		{"sadd-multi", [][]byte{[]byte("SADD"), []byte("s"), []byte("a"), []byte("b")}, false},
		{"zadd-options", [][]byte{[]byte("ZADD"), []byte("z"), []byte("NX"), []byte("1"), []byte("m")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConcurrentScalarCommand(tc.args); got != tc.want {
				t.Fatalf("isConcurrentScalarCommand(%q)=%v want=%v", tc.args, got, tc.want)
			}
		})
	}
}
