package engine

import "time"

// Victim chooses the oldest sampled key, retaining full key equality. The caller
// may exclude keys in the pending mutation so failed writes preserve their data.
func (s *Store) Victim(excluded map[string]bool, volatile bool) (string, bool) {
	var oldest time.Time
	chosen := ""
	found := false
	for _, key := range s.SampleKeys(128) {
		if excluded[key] {
			continue
		}
		sh := s.shardFor(key)
		sh.mu.RLock()
		e, ok := sh.get(key)
		if ok && (!volatile || !e.expiresAt.IsZero()) {
			age := e.lastAccess.Time()
			if e.expired(s.now()) {
				age = time.Time{}
			}
			if !found || age.Before(oldest) {
				chosen = key
				oldest = age
				found = true
			}
		}
		sh.mu.RUnlock()
	}
	return chosen, found
}
