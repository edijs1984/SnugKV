package engine

import "time"

// stamp stores absolute milliseconds, matching the protocol and persistence.
type stamp int64

func stampOf(t time.Time) stamp {
	if t.IsZero() {
		return 0
	}
	return stamp(t.UnixMilli())
}
func (t stamp) IsZero() bool { return t == 0 }
func (t stamp) Time() time.Time {
	if t == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(t))
}
func (t stamp) UnixMilli() int64                  { return int64(t) }
func (t stamp) Sub(other time.Time) time.Duration { return t.Time().Sub(other) }
