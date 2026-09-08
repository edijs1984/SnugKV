package stats

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestHistogram(t *testing.T) {
	r := New()
	r.Observe("GET", time.Millisecond, false)
	r.Observe("GET", 2*time.Second, true)
	var b bytes.Buffer
	r.WritePrometheus(&b)
	for _, part := range []string{`snugkv_commands_total{command="GET"} 2`, `snugkv_command_errors_total{command="GET"} 1`, `le="0.001"} 1`, `le="+Inf"} 2`} {
		if !strings.Contains(b.String(), part) {
			t.Fatalf("missing %s: %s", part, b.String())
		}
	}
}
