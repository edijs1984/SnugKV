package arena

// Generation returns the allocation generation for optimistic value CAS checks.
// It changes whenever a key publishes a new arena value within the owning shard.
func (r Ref) Generation() uint64 {
	if r.IsInline() {
		return r.generation & inlineGenerationMask
	}
	return r.generation
}

// Words returns the two machine words that make up the reference, so a caller
// can store it in a packed form. The generation word is at most GenerationBits
// wide for references this arena produced.
func (r Ref) Words() (location, generation uint64) {
	return r.location, r.generation
}

// RefFromWords rebuilds a reference from the words returned by Words.
func RefFromWords(location, generation uint64) Ref {
	return Ref{location: location, generation: generation}
}
