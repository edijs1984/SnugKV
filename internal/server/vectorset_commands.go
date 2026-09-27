package server

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
)

var vectorSetCommands = map[string]commandInfo{
	"VADD":      {5, 0, 1, 1, 1, true},
	"VCARD":     {2, 2, 1, 1, 1, false},
	"VDIM":      {2, 2, 1, 1, 1, false},
	"VEMB":      {3, 4, 1, 1, 1, false},
	"VISMEMBER": {3, 3, 1, 1, 1, false},
	"VREM":      {3, 3, 1, 1, 1, true},
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
	}

	return nil, errors.New("ERR unknown vector set command")
}
