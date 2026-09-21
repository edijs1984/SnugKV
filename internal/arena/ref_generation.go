package arena

// Generation returns the allocation generation for optimistic value CAS checks.
// It changes whenever a key publishes a new arena value within the owning shard.
func (r Ref) Generation() uint64 {
	if r.IsInline() {
		return r.generation & inlineGenerationMask
	}
	return r.generation
}
