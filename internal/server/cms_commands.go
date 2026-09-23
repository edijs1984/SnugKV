package server

import (
	"errors"
	"strconv"
	"strings"
)

var cmsCommands = map[string]commandInfo{
	"CMS.INITBYDIM":  {4, 4, 1, 1, 1, true},
	"CMS.INITBYPROB": {4, 4, 1, 1, 1, true},
	"CMS.INCRBY":     {4, 0, 1, 1, 1, true},
	"CMS.QUERY":      {3, 0, 1, 1, 1, false},
	"CMS.MERGE":      {4, 0, 1, 1, 1, true},
	"CMS.INFO":       {2, 2, 1, 1, 1, false},
}

func init() {
	for name, info := range cmsCommands {
		commandTable[name] = info
	}
}

func isCMSCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := cmsCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func cmsIntegerArray(values []uint64) []byte {
	items := make([][]byte, len(values))
	for i, value := range values {
		items[i] = integer(int64(value))
	}
	return array(items...)
}

func parseCMSIncrements(args [][]byte) ([][]byte, []uint64, error) {
	if len(args) < 4 || len(args)%2 != 0 {
		return nil, nil, errors.New("ERR wrong number of arguments for 'cms.incrby' command")
	}
	items := make([][]byte, 0, (len(args)-2)/2)
	values := make([]uint64, 0, (len(args)-2)/2)
	for pos := 2; pos < len(args); pos += 2 {
		value, err := strconv.ParseInt(string(args[pos+1]), 10, 64)
		if err != nil {
			return nil, nil, errors.New("CMS: Cannot parse number")
		}
		if value < 0 {
			return nil, nil, errors.New("CMS: Number cannot be negative")
		}
		items = append(items, args[pos])
		values = append(values, uint64(value))
	}
	return items, values, nil
}

func (s *Server) executeCMS(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := cmsCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown CMS command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	key := string(args[1])
	switch cmd {
	case "CMS.INITBYDIM":
		width, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil || width < 1 {
			return nil, errors.New("CMS: invalid width")
		}
		depth, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil || depth < 1 {
			return nil, errors.New("CMS: invalid depth")
		}
		if err := s.store.CMSInitByDim(key, uint64(width), uint64(depth)); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "CMS.INITBYPROB":
		overestimation, err := strconv.ParseFloat(string(args[2]), 64)
		if err != nil || !(overestimation > 0 && overestimation < 1) {
			return nil, errors.New("CMS: invalid overestimation value")
		}
		probability, err := strconv.ParseFloat(string(args[3]), 64)
		if err != nil || !(probability > 0 && probability < 1) {
			return nil, errors.New("CMS: invalid prob value")
		}
		if err := s.store.CMSInitByProb(key, overestimation, probability); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "CMS.INCRBY":
		items, values, err := parseCMSIncrements(args)
		if err != nil {
			return nil, err
		}
		results, err := s.store.CMSIncrBy(key, items, values)
		if err != nil {
			return nil, err
		}
		return cmsIntegerArray(results), nil

	case "CMS.QUERY":
		results, err := s.store.CMSQuery(key, args[2:])
		if err != nil {
			return nil, err
		}
		return cmsIntegerArray(results), nil

	case "CMS.INFO":
		info, err := s.store.CMSInfo(key)
		if err != nil {
			return nil, err
		}
		// The audited RedisBloom build reports only width/depth/count in RESP2.
		return array(
			[]byte("+width\r\n"), integer(int64(info.Width)),
			[]byte("+depth\r\n"), integer(int64(info.Depth)),
			[]byte("+count\r\n"), integer(int64(info.Count)),
		), nil

	case "CMS.MERGE":
		// RedisBloom checks the destination before parsing the remaining merge
		// arguments, so preserve that observable error ordering.
		if _, err := s.store.CMSInfo(key); err != nil {
			return nil, err
		}
		numKeys, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("CMS: invalid numkeys")
		}
		if numKeys <= 0 {
			return nil, errors.New("CMS: Number of keys must be positive")
		}

		n := int(numKeys)
		if len(args) < 3+n {
			return nil, errors.New("CMS: wrong number of keys")
		}
		sources := make([]string, n)
		for i := 0; i < n; i++ {
			sources[i] = string(args[3+i])
		}
		weights := make([]int64, n)
		for i := range weights {
			weights[i] = 1
		}

		afterSources := 3 + n
		if len(args) == afterSources {
			// Unweighted merge.
		} else {
			if len(args) <= afterSources ||
				!strings.EqualFold(string(args[afterSources]), "WEIGHTS") ||
				len(args) != afterSources+1+n {
				return nil, errors.New("CMS: wrong number of keys/weights")
			}
			for i := 0; i < n; i++ {
				weight, err := strconv.ParseInt(string(args[afterSources+1+i]), 10, 64)
				if err != nil {
					return nil, errors.New("CMS: invalid weight value")
				}
				weights[i] = weight
			}
		}
		if err := s.store.CMSMerge(key, sources, weights); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}
	return nil, errors.New("ERR unknown CMS command")
}
