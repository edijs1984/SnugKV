package engine

// RedisGlobMatch exposes the byte-oriented Redis glob matcher to protocol
// features outside the engine package, such as Pub/Sub pattern subscriptions.
// It preserves the same binary-safe MATCH semantics used by SCAN-family commands.
func RedisGlobMatch(pattern, value []byte) bool {
	return redisGlobMatch(pattern, value)
}
