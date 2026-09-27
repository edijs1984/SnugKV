package server

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strconv"
	"time"
	"strings"
)

var vectorSetCommands = map[string]commandInfo{
	"VADD":      {5, 0, 1, 1, 1, true},
	"VCARD":     {2, 2, 1, 1, 1, false},
	"VDIM":      {2, 2, 1, 1, 1, false},
	"VEMB":      {3, 4, 1, 1, 1, false},
	"VISMEMBER": {3, 3, 1, 1, 1, false},
	"VREM":        {3, 3, 1, 1, 1, true},
	"VGETATTR":    {3, 3, 1, 1, 1, false},
	"VSETATTR":    {4, 4, 1, 1, 1, true},
	"VRANDMEMBER": {2, 3, 1, 1, 1, false},
	"VINFO":       {2, 2, 1, 1, 1, false},
	"VLINKS":      {3, 4, 1, 1, 1, false},
	"VSIM":        {4, 0, 1, 1, 1, false},
}

func init() {
	for name, info := range vectorSetCommands {
		commandTable[name] = info
	}
}

func parseVAdd(args [][]byte) (key, element string, vector []float64, attrs []byte, err error) {
	if len(args) < 5 {
		err = errors.New("ERR wrong number of arguments for 'vadd' command")
		return
	}
	key = string(args[1])
	i := 2

	if strings.EqualFold(string(args[i]), "REDUCE") {
		if i+1 >= len(args) {
			err = errors.New("ERR syntax error")
			return
		}
		dim, parseErr := strconv.Atoi(string(args[i+1]))
		if parseErr != nil || dim <= 0 {
			err = errors.New("ERR invalid REDUCE dimension")
			return
		}
		// REDUCE is syntax-compatible in this core batch. Random projection is
		// introduced with the HNSW/search layer; the supplied vector is stored.
		i += 2
	}

	if i >= len(args) {
		err = errors.New("ERR syntax error")
		return
	}

	switch strings.ToUpper(string(args[i])) {
	case "VALUES":
		if i+1 >= len(args) {
			err = errors.New("ERR syntax error")
			return
		}
		n, parseErr := strconv.Atoi(string(args[i+1]))
		if parseErr != nil || n <= 0 || i+2+n >= len(args) {
			err = errors.New("ERR invalid vector")
			return
		}
		vector = make([]float64, n)
		for j := 0; j < n; j++ {
			value, parseErr := strconv.ParseFloat(string(args[i+2+j]), 64)
			if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				err = errors.New("ERR invalid vector value")
				return
			}
			vector[j] = value
		}
		i += 2 + n

	case "FP32":
		if i+2 >= len(args) {
			err = errors.New("ERR syntax error")
			return
		}
		blob := args[i+1]
		if len(blob) == 0 || len(blob)%4 != 0 {
			err = errors.New("ERR invalid FP32 vector")
			return
		}
		vector = make([]float64, len(blob)/4)
		for j := range vector {
			bits := binary.LittleEndian.Uint32(blob[j*4 : j*4+4])
			value := math.Float32frombits(bits)
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				err = errors.New("ERR invalid vector value")
				return
			}
			vector[j] = float64(value)
		}
		i += 2

	default:
		err = errors.New("ERR syntax error")
		return
	}

	if i >= len(args) {
		err = errors.New("ERR syntax error")
		return
	}
	element = string(args[i])
	i++

	quantSeen := false
	for i < len(args) {
		switch strings.ToUpper(string(args[i])) {
		case "CAS":
			i++
		case "NOQUANT", "Q8", "BIN":
			if quantSeen {
				err = errors.New("ERR syntax error")
				return
			}
			quantSeen = true
			i++
		case "EF", "M":
			if i+1 >= len(args) {
				err = errors.New("ERR syntax error")
				return
			}
			n, parseErr := strconv.Atoi(string(args[i+1]))
			if parseErr != nil || n <= 0 {
				err = errors.New("ERR value is not an integer or out of range")
				return
			}
			i += 2
		case "SETATTR":
			if i+1 >= len(args) {
				err = errors.New("ERR syntax error")
				return
			}
			attrs = append([]byte(nil), args[i+1]...)
			i += 2
		default:
			err = errors.New("ERR syntax error")
			return
		}
	}
	return
}

func vectorFloatReply(vector []float32) []byte {
	items := make([][]byte, len(vector))
	for i, value := range vector {
		items[i] = formatBulkString([]byte(
			strconv.FormatFloat(float64(value), 'g', -1, 32),
		))
	}
	return array(items...)
}

func vectorRawReply(vector []float32) []byte {
	blob := make([]byte, len(vector)*4)
	var normSquared float64
	for i, value := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:i*4+4], math.Float32bits(value))
		normSquared += float64(value) * float64(value)
	}
	return array(
		[]byte("+fp32\r\n"),
		formatBulkString(blob),
		[]byte("+"+strconv.FormatFloat(math.Sqrt(normSquared), 'g', -1, 64)+"\r\n"),
	)
}


func parseVSimQuery(s *Server, args [][]byte) (key string, query []float64, next int, err error) {
	if len(args) < 4 {
		return "", nil, 0, errors.New("ERR wrong number of arguments for 'vsim' command")
	}
	key = string(args[1])

	info, found, infoErr := s.store.VectorSetInfo(key)
	if infoErr != nil {
		return "", nil, 0, infoErr
	}
	if !found {
		// Unknown key is not an error for VSIM. Return next=-1 as sentinel.
		return key, nil, -1, nil
	}

	switch strings.ToUpper(string(args[2])) {
	case "ELE":
		vector, found, embErr := s.store.VectorSetEmb(key, string(args[3]))
		if embErr != nil {
			return "", nil, 0, embErr
		}
		if !found {
			return "", nil, 0, errors.New("ERR element not found")
		}
		query = make([]float64, len(vector))
		for i, value := range vector {
			query[i] = float64(value)
		}
		next = 4

	case "FP32":
		blob := args[3]
		if len(blob) == 0 || len(blob)%4 != 0 {
			return "", nil, 0, errors.New("ERR invalid FP32 vector")
		}
		query = make([]float64, len(blob)/4)
		for i := range query {
			value := math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4 : i*4+4]))
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return "", nil, 0, errors.New("ERR invalid vector value")
			}
			query[i] = float64(value)
		}
		next = 4

	case "VALUES":
		if len(args) < 5 {
			return "", nil, 0, errors.New("ERR syntax error")
		}
		n, parseErr := strconv.Atoi(string(args[3]))
		if parseErr != nil || n <= 0 || 4+n > len(args) {
			return "", nil, 0, errors.New("ERR invalid vector")
		}
		query = make([]float64, n)
		for i := 0; i < n; i++ {
			value, parseErr := strconv.ParseFloat(string(args[4+i]), 64)
			if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return "", nil, 0, errors.New("ERR invalid vector value")
			}
			query[i] = value
		}
		next = 4 + n

	default:
		return "", nil, 0, errors.New("ERR syntax error")
	}

	if int64(len(query)) != info.Dim {
		return "", nil, 0, errors.New("ERR vector dimension mismatch")
	}
	return key, query, next, nil
}

func vectorAttrValue(attrs []byte, field string) (any, bool) {
	if len(attrs) == 0 {
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal(attrs, &object); err != nil {
		return nil, false
	}
	value, ok := object[field]
	return value, ok
}

func parseVectorFilterLiteral(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "true" {
		return true
	}
	if raw == "false" {
		return false
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var value string
		if json.Unmarshal([]byte(raw), &value) == nil {
			return value
		}
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return n
	}
	return raw
}

func compareVectorFilter(left any, op string, right any) bool {
	lf, lok := left.(float64)
	rf, rok := right.(float64)
	if lok && rok {
		switch op {
		case "==":
			return lf == rf
		case "!=":
			return lf != rf
		case ">":
			return lf > rf
		case "<":
			return lf < rf
		case ">=":
			return lf >= rf
		case "<=":
			return lf <= rf
		}
	}
	ls, lok := left.(string)
	rs, rok := right.(string)
	if lok && rok {
		switch op {
		case "==":
			return ls == rs
		case "!=":
			return ls != rs
		case ">":
			return ls > rs
		case "<":
			return ls < rs
		case ">=":
			return ls >= rs
		case "<=":
			return ls <= rs
		}
	}
	lb, lok := left.(bool)
	rb, rok := right.(bool)
	if lok && rok {
		switch op {
		case "==":
			return lb == rb
		case "!=":
			return lb != rb
		}
	}
	return false
}

func evalVectorFilterTerm(attrs []byte, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" {
		return false
	}
	for _, op := range []string{">=", "<=", "==", "!=", ">", "<"} {
		if idx := strings.Index(term, op); idx > 0 {
			left := strings.TrimSpace(term[:idx])
			right := strings.TrimSpace(term[idx+len(op):])
			if !strings.HasPrefix(left, ".") || len(left) < 2 {
				return false
			}
			value, ok := vectorAttrValue(attrs, left[1:])
			if !ok {
				return false
			}
			return compareVectorFilter(value, op, parseVectorFilterLiteral(right))
		}
	}
	return false
}

func evalVectorFilter(attrs []byte, expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

	// Core filtered-search support: top-level scalar comparisons combined with
	// AND/OR (including && / ||). More advanced arithmetic/in syntax is left
	// for the later exhaustive compatibility pass.
	orParts := strings.FieldsFunc(expr, func(r rune) bool { return r == '|' })
	if strings.Contains(expr, "||") && len(orParts) > 1 {
		for _, part := range strings.Split(expr, "||") {
			if evalVectorFilter(attrs, part) {
				return true
			}
		}
		return false
	}
	lower := strings.ToLower(expr)
	if strings.Contains(lower, " or ") {
		parts := strings.Split(lower, " or ")
		offset := 0
		for _, part := range parts {
			idx := strings.Index(strings.ToLower(expr[offset:]), part)
			if idx < 0 {
				continue
			}
			actual := expr[offset+idx : offset+idx+len(part)]
			if evalVectorFilter(attrs, actual) {
				return true
			}
			offset += idx + len(part) + 4
		}
		return false
	}

	if strings.Contains(expr, "&&") {
		for _, part := range strings.Split(expr, "&&") {
			if !evalVectorFilterTerm(attrs, part) {
				return false
			}
		}
		return true
	}
	if strings.Contains(lower, " and ") {
		parts := strings.Split(expr, " and ")
		if len(parts) == 1 {
			parts = strings.Split(expr, " AND ")
		}
		for _, part := range parts {
			if !evalVectorFilterTerm(attrs, part) {
				return false
			}
		}
		return true
	}
	return evalVectorFilterTerm(attrs, expr)
}

func vectorScoreBulk(score float64) []byte {
	return formatBulkString([]byte(strconv.FormatFloat(score, 'g', -1, 64)))
}

func (s *Server) executeVectorSet(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	key := string(args[1])

	switch cmd {
	case "VADD":
		key, element, vector, attrs, err := parseVAdd(args)
		if err != nil {
			return nil, err
		}
		added, err := s.store.VectorSetAdd(key, element, vector, attrs)
		if err != nil {
			return nil, err
		}
		return boolean(added), nil

	case "VCARD":
		n, err := s.store.VectorSetCard(key)
		return integer(n), err

	case "VDIM":
		n, err := s.store.VectorSetDim(key)
		return integer(n), err

	case "VEMB":
		vector, found, err := s.store.VectorSetEmb(key, string(args[2]))
		if err != nil {
			return nil, err
		}
		if !found {
			return array(), nil
		}
		if len(args) == 4 {
			if !strings.EqualFold(string(args[3]), "RAW") {
				return nil, errors.New("ERR syntax error")
			}
			return vectorRawReply(vector), nil
		}
		return vectorFloatReply(vector), nil

	case "VISMEMBER":
		found, err := s.store.VectorSetIsMember(key, string(args[2]))
		if err != nil {
			return nil, err
		}
		return boolean(found), nil

	case "VREM":
		removed, err := s.store.VectorSetRemove(key, string(args[2]))
		if err != nil {
			return nil, err
		}
		return boolean(removed), nil

	case "VGETATTR":
		attrs, found, err := s.store.VectorSetGetAttr(key, string(args[2]))
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return []byte("+" + string(attrs) + "\r\n"), nil

	case "VSETATTR":
		updated, err := s.store.VectorSetSetAttr(key, string(args[2]), args[3])
		if err != nil {
			return nil, err
		}
		return boolean(updated), nil

	case "VRANDMEMBER":
		names, found, err := s.store.VectorSetMembers(key)
		if err != nil {
			return nil, err
		}
		if len(args) == 2 {
			if !found || len(names) == 0 {
				return nullBulk(), nil
			}
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			return formatBulkString([]byte(names[r.Intn(len(names))])), nil
		}

		count, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		if !found || len(names) == 0 || count == 0 {
			return array(), nil
		}
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		items := make([][]byte, 0)

		if count > 0 {
			limit := int(count)
			if limit > len(names) {
				limit = len(names)
			}
			perm := r.Perm(len(names))
			for _, idx := range perm[:limit] {
				items = append(items, formatBulkString([]byte(names[idx])))
			}
		} else {
			limit := int(-count)
			for i := 0; i < limit; i++ {
				items = append(items, formatBulkString([]byte(names[r.Intn(len(names))])))
			}
		}
		return array(items...), nil

	case "VINFO":
		info, found, err := s.store.VectorSetInfo(key)
		if err != nil {
			return nil, err
		}
		if !found {
			return []byte("*-1\r\n"), nil
		}
		return array(
			[]byte("+quant-type\r\n"), []byte("+fp32\r\n"),
			[]byte("+vector-dim\r\n"), integer(info.Dim),
			[]byte("+size\r\n"), integer(info.Size),
			[]byte("+max-level\r\n"), integer(0),
			[]byte("+vset-uid\r\n"), integer(1),
			[]byte("+hnsw-max-node-uid\r\n"), integer(info.Size),
		), nil

	case "VLINKS":
		withScores := false
		if len(args) == 4 {
			if !strings.EqualFold(string(args[3]), "WITHSCORES") {
				return nil, errors.New("ERR syntax error")
			}
			withScores = true
		}
		links, found, err := s.store.VectorSetLinks(key, string(args[2]), 16)
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		layer := make([][]byte, 0, len(links)*2)
		for _, item := range links {
			layer = append(layer, formatBulkString([]byte(item.Name)))
			if withScores {
				layer = append(layer, vectorScoreBulk(item.Score))
			}
		}
		return array(array(layer...)), nil

	case "VSIM":
		key, query, next, err := parseVSimQuery(s, args)
		if err != nil {
			return nil, err
		}
		if next == -1 {
			return array(), nil
		}

		withScores := false
		withAttribs := false
		count := 10
		epsilon := -1.0
		filterExpr := ""

		for i := next; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "WITHSCORES":
				withScores = true
				i++
			case "WITHATTRIBS":
				withAttribs = true
				i++
			case "COUNT":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, parseErr := strconv.Atoi(string(args[i+1]))
				if parseErr != nil || n <= 0 {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				count = n
				i += 2
			case "EPSILON":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				value, parseErr := strconv.ParseFloat(string(args[i+1]), 64)
				if parseErr != nil || value < 0 || value > 1 {
					return nil, errors.New("ERR invalid epsilon")
				}
				epsilon = value
				i += 2
			case "EF", "FILTER-EF":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, parseErr := strconv.Atoi(string(args[i+1]))
				if parseErr != nil || n < 0 {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				i += 2
			case "FILTER":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				filterExpr = string(args[i+1])
				i += 2
			case "TRUTH", "NOTHREAD":
				i++
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		var filter func([]byte) bool
		if filterExpr != "" {
			filter = func(attrs []byte) bool {
				return evalVectorFilter(attrs, filterExpr)
			}
		}
		results, _, err := s.store.VectorSetSearch(key, query, filter)
		if err != nil {
			return nil, err
		}

		items := make([][]byte, 0, count*3)
		for _, item := range results {
			if epsilon >= 0 && item.Score < 1-epsilon {
				continue
			}
			items = append(items, formatBulkString([]byte(item.Name)))
			if withScores {
				items = append(items, vectorScoreBulk(item.Score))
			}
			if withAttribs {
				if len(item.Attrs) == 0 {
					items = append(items, nullBulk())
				} else {
					items = append(items, formatBulkString(item.Attrs))
				}
			}
			if len(items) >= count*(1+btoi(withScores)+btoi(withAttribs)) {
				break
			}
		}
		return array(items...), nil
	}

	return nil, errors.New("ERR unknown vector set command")
}
