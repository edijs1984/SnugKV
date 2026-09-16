package engine

import (
	"container/heap"
)

type expiration struct {
	key string
	at  stamp
}
type expirationQueue struct {
	items     []expiration
	positions map[string]int
}

func (q expirationQueue) Len() int           { return len(q.items) }
func (q expirationQueue) Less(i, j int) bool { return q.items[i].at < q.items[j].at }
func (q expirationQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.positions[q.items[i].key] = i
	q.positions[q.items[j].key] = j
}
func (q *expirationQueue) Push(v interface{}) {
	e := v.(expiration)
	q.positions[e.key] = len(q.items)
	q.items = append(q.items, e)
}
func (q *expirationQueue) Pop() interface{} {
	last := len(q.items) - 1
	e := q.items[last]
	q.items[last] = expiration{}
	q.items = q.items[:last]
	delete(q.positions, e.key)
	return e
}

func (q *expirationQueue) atFor(key string) (stamp, bool) {
	if q.positions == nil {
		return 0, false
	}
	i, ok := q.positions[key]
	if !ok || i < 0 || i >= len(q.items) || q.items[i].key != key {
		return 0, false
	}
	return q.items[i].at, true
}

func (sh *shard) schedule(key string, at stamp) {
	if at.IsZero() {
		// Persistent writes are the common case. Do not allocate a positions
		// map merely to discover that there is no scheduled expiry to remove.
		if sh.expiration.positions == nil {
			return
		}
		if i, exists := sh.expiration.positions[key]; exists {
			heap.Remove(&sh.expiration, i)
		}
		return
	}

	if sh.expiration.positions == nil {
		sh.expiration.positions = make(map[string]int)
	}
	if i, exists := sh.expiration.positions[key]; exists {
		sh.expiration.items[i].at = at
		heap.Fix(&sh.expiration, i)
	} else {
		heap.Push(&sh.expiration, expiration{key, at})
	}
}

// CleanupExpiredLimit bounds work by removed entries and shard count. Indexed
// heaps contain at most one record per expiring key, so TTL churn stays bounded.
func (s *Store) CleanupExpiredLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	removed := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		now := s.now()
		for sh.expiration.Len() > 0 && removed < limit {
			next := sh.expiration.items[0]
			if stampOf(now) < next.at {
				break
			}
			s.remove(sh, next.key)
			removed++
		}
		sh.mu.Unlock()
		if removed >= limit {
			break
		}
	}
	return removed
}
