package server

import (
	"crypto/rand"
	"errors"
	"math"
	"math/big"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

const maxHRandFieldCount = int64(1_000_000)

func randomHashIndex(n int) (int, error) {
	if n <= 0 {
		return 0, errors.New("ERR empty hash")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, errors.New("ERR random source unavailable")
	}
	return int(v.Int64()), nil
}

func shuffledHashPrefix(pairs []engine.HashPair, count int) ([]engine.HashPair, error) {
	copyPairs := append([]engine.HashPair(nil), pairs...)
	if count > len(copyPairs) {
		count = len(copyPairs)
	}
	for i := 0; i < count; i++ {
		offset, err := randomHashIndex(len(copyPairs) - i)
		if err != nil {
			return nil, err
		}
		j := i + offset
		copyPairs[i], copyPairs[j] = copyPairs[j], copyPairs[i]
	}
	return copyPairs[:count], nil
}

func (s *Server) executeHRandField(args [][]byte) ([]byte, error) {
	key := string(args[1])
	pairs, err := s.store.HashGetAll(key)
	if err != nil {
		return nil, err
	}

	if len(args) == 2 {
		if len(pairs) == 0 {
			return nullBulk(), nil
		}
		idx, err := randomHashIndex(len(pairs))
		if err != nil {
			return nil, err
		}
		return formatBulkString(pairs[idx].Field), nil
	}

	count, err := strconv.ParseInt(string(args[2]), 10, 64)
	if err != nil {
		return nil, errors.New("ERR value is not an integer or out of range")
	}

	withValues := false
	if len(args) == 4 {
		if !strings.EqualFold(string(args[3]), "WITHVALUES") {
			return nil, errors.New("ERR syntax error")
		}
		withValues = true
	}

	if len(pairs) == 0 || count == 0 {
		return array(), nil
	}

	selected := make([]engine.HashPair, 0)
	if count > 0 {
		want := count
		if want > int64(len(pairs)) {
			want = int64(len(pairs))
		}
		selected, err = shuffledHashPrefix(pairs, int(want))
		if err != nil {
			return nil, err
		}
	} else {
		if count == math.MinInt64 || -count > maxHRandFieldCount {
			return nil, errors.New("ERR count is too large")
		}
		want := int(-count)
		selected = make([]engine.HashPair, 0, want)
		for i := 0; i < want; i++ {
			idx, err := randomHashIndex(len(pairs))
			if err != nil {
				return nil, err
			}
			selected = append(selected, pairs[idx])
		}
	}

	width := 1
	if withValues {
		width = 2
	}
	items := make([][]byte, 0, width*len(selected))
	for _, pair := range selected {
		items = append(items, formatBulkString(pair.Field))
		if withValues {
			items = append(items, formatBulkString(pair.Value))
		}
	}
	return array(items...), nil
}
