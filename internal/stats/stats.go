// Package stats provides bounded command-label metrics without external dependencies.
package stats

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

var bounds = []time.Duration{100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second}

type Command struct {
	Count, Errors uint64
	Nanoseconds   uint64
	Buckets       [6]uint64
}
type Registry struct {
	mu       sync.Mutex
	commands map[string]Command
}

func New() *Registry { return &Registry{commands: make(map[string]Command)} }
func (r *Registry) Observe(name string, d time.Duration, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.commands[name]
	c.Count++
	if failed {
		c.Errors++
	}
	c.Nanoseconds += uint64(d)
	for i, b := range bounds {
		if d <= b {
			c.Buckets[i]++
		}
	}
	c.Buckets[5]++
	r.commands[name] = c
}
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.commands))
	for name := range r.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := r.commands[name]
		fmt.Fprintf(w, "morphcache_commands_total{command=%q} %d\nmorphcache_command_errors_total{command=%q} %d\n", name, c.Count, name, c.Errors)
		for i, b := range bounds {
			fmt.Fprintf(w, "morphcache_command_duration_seconds_bucket{command=%q,le=%q} %d\n", name, fmt.Sprint(b.Seconds()), c.Buckets[i])
		}
		fmt.Fprintf(w, "morphcache_command_duration_seconds_bucket{command=%q,le=\"+Inf\"} %d\nmorphcache_command_duration_seconds_sum{command=%q} %f\nmorphcache_command_duration_seconds_count{command=%q} %d\n", name, c.Count, name, float64(c.Nanoseconds)/1e9, name, c.Count)
	}
}
