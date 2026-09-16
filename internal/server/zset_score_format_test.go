package server

import (
	"math"
	"testing"

	"snugkv/internal/engine"
)

func TestFormatZSetScoreMatchesRedisIntegerFormatting(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  string
	}{
		{name: "geo-sized integer", score: 3479099956230698, want: "3479099956230698"},
		{name: "fractional", score: 56.4412578701582, want: "56.4412578701582"},
		{name: "zero", score: 0, want: "0"},
		{name: "negative zero", score: math.Copysign(0, -1), want: "-0"},
		{name: "positive infinity", score: math.Inf(1), want: "inf"},
		{name: "negative infinity", score: math.Inf(-1), want: "-inf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(formatZSetScore(tt.score)); got != tt.want {
				t.Fatalf("formatZSetScore(%v) = %q, want %q", tt.score, got, tt.want)
			}
		})
	}
}

func TestZSetLargeIntegerScoreWireFormatting(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "ZADD", "geo", "3479099956230698", "Palermo"); got != ":1\r\n" {
		t.Fatalf("ZADD = %q", got)
	}
	if got := execute(t, s, "ZSCORE", "geo", "Palermo"); got != "$16\r\n3479099956230698\r\n" {
		t.Fatalf("ZSCORE = %q", got)
	}
	if got := execute(t, s, "ZRANGE", "geo", "0", "-1", "WITHSCORES"); got != "*2\r\n$7\r\nPalermo\r\n$16\r\n3479099956230698\r\n" {
		t.Fatalf("ZRANGE WITHSCORES = %q", got)
	}
}
