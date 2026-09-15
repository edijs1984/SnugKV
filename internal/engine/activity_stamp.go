package engine

import (
	"math"
	"time"
)

// activityStamp stores coarse optimizer/access timestamps in Unix seconds.
//
// TTL/expiration keeps using the full-width stamp type. Activity timestamps are
// only used for minute-scale heat and rewrite cooldown decisions, so second
// precision is sufficient while cutting these four per-entry fields in half.
type activityStamp uint32

func activityStampOf(t time.Time) activityStamp {
	seconds := t.Unix()
	if seconds <= 0 {
		// Zero is reserved for "not recorded". SnugKV operates on contemporary
		// wall-clock times, but keep pre-epoch/custom test clocks representable.
		return 1
	}
	if seconds >= math.MaxUint32 {
		return activityStamp(math.MaxUint32)
	}
	return activityStamp(seconds)
}

func (s activityStamp) IsZero() bool { return s == 0 }

func (s activityStamp) Time() time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(int64(s), 0)
}
