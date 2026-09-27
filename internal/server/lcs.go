package server

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

type lcsMatch struct {
	aStart int
	aEnd   int
	bStart int
	bEnd   int
	length int
}

type lcsOptions struct {
	lengthOnly   bool
	indexes      bool
	minMatchLen  int64
	withMatchLen bool
}

func parseLCSOptions(args [][]byte) (lcsOptions, error) {
	var options lcsOptions

	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "IDX":
			options.indexes = true
		case "LEN":
			options.lengthOnly = true
		case "WITHMATCHLEN":
			options.withMatchLen = true
		case "MINMATCHLEN":
			if i+1 >= len(args) {
				return lcsOptions{}, errors.New("ERR syntax error")
			}
			value, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil {
				return lcsOptions{}, errors.New("ERR value is not an integer or out of range")
			}
			if value < 0 {
				value = 0
			}
			options.minMatchLen = value
			i++
		default:
			return lcsOptions{}, errors.New("ERR syntax error")
		}
	}

	if options.lengthOnly && options.indexes {
		return lcsOptions{}, errors.New("ERR If you want both the length and indexes, please just use IDX.")
	}

	return options, nil
}

func buildLCSTable(a, b []byte) ([]uint32, int, error) {
	rows := len(a) + 1
	cols := len(b) + 1

	if rows <= 0 || cols <= 0 || rows > math.MaxInt/cols {
		return nil, 0, errors.New("ERR String too long for LCS")
	}
	cells := rows * cols
	if cells > math.MaxInt/4 {
		return nil, 0, errors.New("ERR Insufficient memory, failed allocating transient memory for LCS")
	}

	// Keep transient LCS memory bounded. Redis compares this allocation to
	// proto-max-bulk-len; SnugKV currently has no identical server-wide setting.
	const maxLCSTableBytes = 512 << 20
	if cells*4 > maxLCSTableBytes {
		return nil, 0, errors.New("ERR Insufficient memory, transient memory for LCS exceeds proto-max-bulk-len")
	}

	table := make([]uint32, cells)
	at := func(i, j int) int { return j + i*cols }

	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			if a[i-1] == b[j-1] {
				table[at(i, j)] = table[at(i-1, j-1)] + 1
				continue
			}

			up := table[at(i-1, j)]
			left := table[at(i, j-1)]
			if up > left {
				table[at(i, j)] = up
			} else {
				// Redis deliberately prefers the left cell on ties.
				table[at(i, j)] = left
			}
		}
	}

	return table, cols, nil
}

func lcsResult(a, b []byte, options lcsOptions) ([]byte, []lcsMatch, int, error) {
	table, cols, err := buildLCSTable(a, b)
	if err != nil {
		return nil, nil, 0, err
	}

	at := func(i, j int) uint32 { return table[j+i*cols] }
	total := int(at(len(a), len(b)))
	if options.lengthOnly {
		return nil, nil, total, nil
	}

	result := make([]byte, total)
	idx := total
	i, j := len(a), len(b)

	var matches []lcsMatch
	aStart := len(a)
	aEnd := 0
	bStart := 0
	bEnd := 0

	emit := func() {
		if aStart == len(a) {
			return
		}
		matchLen := aEnd - aStart + 1
		if options.minMatchLen == 0 || int64(matchLen) >= options.minMatchLen {
			if options.indexes {
				matches = append(matches, lcsMatch{
					aStart: aStart,
					aEnd:   aEnd,
					bStart: bStart,
					bEnd:   bEnd,
					length: matchLen,
				})
			}
		}
		aStart = len(a)
	}

	for i > 0 && j > 0 {
		emitRange := false

		if a[i-1] == b[j-1] {
			idx--
			result[idx] = a[i-1]

			if aStart == len(a) {
				aStart = i - 1
				aEnd = i - 1
				bStart = j - 1
				bEnd = j - 1
			} else if aStart == i && bStart == j {
				aStart--
				bStart--
			} else {
				emitRange = true
			}

			if aStart == 0 || bStart == 0 {
				emitRange = true
			}

			i--
			j--
		} else {
			up := at(i-1, j)
			left := at(i, j-1)
			if up > left {
				i--
			} else {
				j--
			}
			if aStart != len(a) {
				emitRange = true
			}
		}

		if emitRange {
			emit()
		}
	}

	emit()
	return result, matches, total, nil
}

func lcsIndexesReply(matches []lcsMatch, total int, withMatchLen bool) []byte {
	items := make([][]byte, 0, len(matches))

	for _, match := range matches {
		fields := [][]byte{
			array(integer(int64(match.aStart)), integer(int64(match.aEnd))),
			array(integer(int64(match.bStart)), integer(int64(match.bEnd))),
		}
		if withMatchLen {
			fields = append(fields, integer(int64(match.length)))
		}
		items = append(items, array(fields...))
	}

	return array(
		formatBulkString([]byte("matches")),
		array(items...),
		formatBulkString([]byte("len")),
		integer(int64(total)),
	)
}

func (s *Server) executeLCS(args [][]byte) ([]byte, error) {
	options, err := parseLCSOptions(args)
	if err != nil {
		return nil, err
	}

	a, foundA, wrongA := s.store.GetString(string(args[1]))
	b, foundB, wrongB := s.store.GetString(string(args[2]))
	if wrongA || wrongB {
		return nil, errors.New("ERR The specified keys must contain string values")
	}
	if !foundA {
		a = []byte{}
	}
	if !foundB {
		b = []byte{}
	}

	result, matches, total, err := lcsResult(a, b, options)
	if err != nil {
		return nil, err
	}

	if options.indexes {
		return lcsIndexesReply(matches, total, options.withMatchLen), nil
	}
	if options.lengthOnly {
		return integer(int64(total)), nil
	}
	return formatBulkString(result), nil
}
