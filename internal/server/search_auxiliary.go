package server

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

type searchSuggestion struct {
	score   float64
	payload []byte
}

func (s *Server) executeFTDict(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments")
	}
	name := string(args[1])

	s.searchAuxMu.Lock()
	defer s.searchAuxMu.Unlock()

	switch cmd {
	case "FT.DICTADD":
		if len(args) < 3 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.dictadd' command")
		}
		dict := s.searchDictionaries[name]
		if dict == nil {
			dict = make(map[string]struct{})
			s.searchDictionaries[name] = dict
		}
		added := 0
		for _, raw := range args[2:] {
			term := string(raw)
			if _, exists := dict[term]; exists {
				continue
			}
			dict[term] = struct{}{}
			added++
		}
		return integer(int64(added)), nil

	case "FT.DICTDEL":
		if len(args) < 3 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.dictdel' command")
		}
		dict := s.searchDictionaries[name]
		deleted := 0
		for _, raw := range args[2:] {
			term := string(raw)
			if _, exists := dict[term]; !exists {
				continue
			}
			delete(dict, term)
			deleted++
		}
		return integer(int64(deleted)), nil

	case "FT.DICTDUMP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.dictdump' command")
		}
		dict := s.searchDictionaries[name]
		terms := make([]string, 0, len(dict))
		for term := range dict {
			terms = append(terms, term)
		}
		sort.Strings(terms)
		items := make([][]byte, 0, len(terms))
		for _, term := range terms {
			items = append(items, formatBulkString([]byte(term)))
		}
		return array(items...), nil
	}
	return nil, errors.New("ERR command unavailable")
}

func searchEditDistance(a, b string) int {
	ar := []rune(strings.ToLower(a))
	br := []rune(strings.ToLower(b))
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			best := prev[j] + 1
			if curr[j-1]+1 < best {
				best = curr[j-1] + 1
			}
			if prev[j-1]+cost < best {
				best = prev[j-1] + cost
			}
			curr[j] = best
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func spellQueryTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '@', ':', '{', '}', '[', ']', '(', ')', '|', '-', '"':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(fields))
	for _, term := range fields {
		term = strings.Trim(term, "%*")
		if term == "" {
			continue
		}
		if _, err := strconv.ParseFloat(term, 64); err == nil {
			continue
		}
		out = append(out, strings.ToLower(term))
	}
	return out
}

func (s *Server) executeFTSpellCheck(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.spellcheck' command")
	}
	indexName := string(args[1])
	terms, totalDocs, ok := s.store.SearchSpellTerms(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	distance := 1
	includeDicts := make([]string, 0)
	excludeDicts := make([]string, 0)
	for pos := 3; pos < len(args); {
		switch strings.ToUpper(string(args[pos])) {
		case "DISTANCE":
			if pos+1 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			n, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || n < 1 || n > 4 {
				return nil, errors.New("ERR invalid distance")
			}
			distance = n
			pos += 2
		case "TERMS":
			if pos+2 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			mode := strings.ToUpper(string(args[pos+1]))
			dict := string(args[pos+2])
			switch mode {
			case "INCLUDE":
				includeDicts = append(includeDicts, dict)
			case "EXCLUDE":
				excludeDicts = append(excludeDicts, dict)
			default:
				return nil, errors.New("ERR syntax error")
			}
			pos += 3
		case "DIALECT":
			if pos+1 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			n, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || (n != 1 && n != 2) {
				return nil, errors.New("ERR unsupported search dialect")
			}
			pos += 2
		default:
			return nil, errors.New("ERR syntax error")
		}
	}

	s.searchAuxMu.RLock()
	for _, dictName := range includeDicts {
		for term := range s.searchDictionaries[dictName] {
			if _, exists := terms[term]; !exists {
				terms[term] = 0
			}
		}
	}
	excluded := make(map[string]struct{})
	for _, dictName := range excludeDicts {
		for term := range s.searchDictionaries[dictName] {
			excluded[term] = struct{}{}
		}
	}
	s.searchAuxMu.RUnlock()

	queryTerms := spellQueryTerms(string(args[2]))
	out := make([][]byte, 0)
	for _, misspelled := range queryTerms {
		if _, exists := terms[misspelled]; exists {
			continue
		}
		type candidate struct {
			term  string
			score float64
		}
		candidates := make([]candidate, 0)
		for term, df := range terms {
			if _, skip := excluded[term]; skip {
				continue
			}
			if searchEditDistance(misspelled, term) > distance {
				continue
			}
			score := 0.0
			if totalDocs > 0 {
				score = float64(df) / float64(totalDocs)
			}
			candidates = append(candidates, candidate{term: term, score: score})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].score == candidates[j].score {
				return candidates[i].term < candidates[j].term
			}
			return candidates[i].score > candidates[j].score
		})
		if len(candidates) == 0 {
			continue
		}
		suggestions := make([][]byte, 0, len(candidates))
		for _, candidate := range candidates {
			suggestions = append(suggestions, array(
				formatBulkString([]byte(strconv.FormatFloat(candidate.score, 'g', -1, 64))),
				formatBulkString([]byte(candidate.term)),
			))
		}
		out = append(out, array(
			formatBulkString([]byte("TERM")),
			formatBulkString([]byte(misspelled)),
			array(suggestions...),
		))
	}
	return array(out...), nil
}

func (s *Server) executeFTSynonym(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments")
	}
	indexName := string(args[1])
	target, ok := s.store.ResolveSearchIndexName(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	s.searchAuxMu.Lock()
	defer s.searchAuxMu.Unlock()

	switch cmd {
	case "FT.SYNUPDATE":
		if len(args) < 4 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.synupdate' command")
		}
		group := string(args[2])
		pos := 3
		if strings.EqualFold(string(args[pos]), "SKIPINITIALSCAN") {
			pos++
		}
		if pos >= len(args) {
			return nil, errors.New("ERR wrong number of arguments for 'ft.synupdate' command")
		}
		if s.searchSynonyms[target] == nil {
			s.searchSynonyms[target] = make(map[string][]string)
		}
		existing := s.searchSynonyms[target][group]
		seen := make(map[string]struct{}, len(existing)+len(args)-pos)
		for _, term := range existing {
			seen[term] = struct{}{}
		}
		for ; pos < len(args); pos++ {
			term := string(args[pos])
			if _, exists := seen[term]; exists {
				continue
			}
			seen[term] = struct{}{}
			existing = append(existing, term)
		}
		s.searchSynonyms[target][group] = existing
		return []byte("+OK\r\n"), nil

	case "FT.SYNDUMP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.syndump' command")
		}
		byTerm := make(map[string][]string)
		for group, terms := range s.searchSynonyms[target] {
			for _, term := range terms {
				byTerm[term] = append(byTerm[term], group)
			}
		}
		terms := make([]string, 0, len(byTerm))
		for term := range byTerm {
			terms = append(terms, term)
		}
		sort.Strings(terms)
		items := make([][]byte, 0, len(terms)*2)
		for _, term := range terms {
			groups := byTerm[term]
			sort.Strings(groups)
			groupItems := make([][]byte, 0, len(groups))
			for _, group := range groups {
				groupItems = append(groupItems, formatBulkString([]byte(group)))
			}
			items = append(items,
				formatBulkString([]byte(term)),
				array(groupItems...),
			)
		}
		return array(items...), nil
	}
	return nil, errors.New("ERR command unavailable")
}

func (s *Server) executeFTSuggestion(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments")
	}
	key := string(args[1])

	s.searchAuxMu.Lock()
	defer s.searchAuxMu.Unlock()

	switch cmd {
	case "FT.SUGADD":
		if len(args) < 4 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.sugadd' command")
		}
		value := string(args[2])
		score, err := strconv.ParseFloat(string(args[3]), 64)
		if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
			return nil, errors.New("ERR invalid score")
		}
		incr := false
		var payload []byte
		for pos := 4; pos < len(args); {
			switch strings.ToUpper(string(args[pos])) {
			case "INCR":
				incr = true
				pos++
			case "PAYLOAD":
				if pos+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				payload = append([]byte(nil), args[pos+1]...)
				pos += 2
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		dict := s.searchSuggestions[key]
		if dict == nil {
			dict = make(map[string]searchSuggestion)
			s.searchSuggestions[key] = dict
		}
		entry := dict[value]
		if incr {
			score += entry.score
		}
		if payload == nil && entry.payload != nil {
			payload = append([]byte(nil), entry.payload...)
		}
		dict[value] = searchSuggestion{score: score, payload: payload}
		return integer(int64(len(dict))), nil

	case "FT.SUGDEL":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.sugdel' command")
		}
		dict := s.searchSuggestions[key]
		if _, exists := dict[string(args[2])]; !exists {
			return integer(0), nil
		}
		delete(dict, string(args[2]))
		return integer(1), nil

	case "FT.SUGLEN":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.suglen' command")
		}
		return integer(int64(len(s.searchSuggestions[key]))), nil

	case "FT.SUGGET":
		if len(args) < 3 {
			return nil, errors.New("ERR wrong number of arguments for 'ft.sugget' command")
		}
		prefix := strings.ToLower(string(args[2]))
		fuzzy := false
		withScores := false
		withPayloads := false
		maxResults := 5
		for pos := 3; pos < len(args); {
			switch strings.ToUpper(string(args[pos])) {
			case "FUZZY":
				fuzzy = true
				pos++
			case "WITHSCORES":
				withScores = true
				pos++
			case "WITHPAYLOADS":
				withPayloads = true
				pos++
			case "MAX":
				if pos+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, err := strconv.Atoi(string(args[pos+1]))
				if err != nil || n < 0 {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				maxResults = n
				pos += 2
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		type candidate struct {
			value string
			entry searchSuggestion
		}
		matches := make([]candidate, 0)
		for value, entry := range s.searchSuggestions[key] {
			lower := strings.ToLower(value)
			match := strings.HasPrefix(lower, prefix)
			if !match && fuzzy {
				n := len([]rune(prefix))
				runes := []rune(lower)
				if n > len(runes) {
					n = len(runes)
				}
				match = searchEditDistance(prefix, string(runes[:n])) <= 1
			}
			if match {
				matches = append(matches, candidate{value: value, entry: entry})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].entry.score == matches[j].entry.score {
				return matches[i].value < matches[j].value
			}
			return matches[i].entry.score > matches[j].entry.score
		})
		if len(matches) > maxResults {
			matches = matches[:maxResults]
		}

		items := make([][]byte, 0, len(matches)*3)
		for _, match := range matches {
			items = append(items, formatBulkString([]byte(match.value)))
			if withScores {
				items = append(items, formatBulkString([]byte(
					strconv.FormatFloat(match.entry.score, 'g', -1, 64),
				)))
			}
			if withPayloads {
				if match.entry.payload == nil {
					items = append(items, nullBulk())
				} else {
					items = append(items, formatBulkString(match.entry.payload))
				}
			}
		}
		return array(items...), nil
	}
	return nil, errors.New("ERR command unavailable")
}
