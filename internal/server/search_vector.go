package server

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

type vectorSearchSpec struct {
	field      engine.SearchField
	k          int
	rangeMax   *float64
	paramName  string
	scoreAlias string
}

type vectorSearchHit struct {
	key      string
	distance float64
}

type vectorSearchOptions struct {
	params       map[string][]byte
	noContent    bool
	sortBy       string
	sortDesc     bool
	returnFields []string
	dialect      int
}

func findVectorField(def engine.SearchDefinition, alias string) (engine.SearchField, bool) {
	for _, field := range def.Fields {
		if field.Alias == alias && field.Kind == engine.SearchFieldVector {
			return field, true
		}
	}
	return engine.SearchField{}, false
}

func parseVectorSearchSpec(query string, def engine.SearchDefinition) (vectorSearchSpec, bool, error) {
	query = strings.TrimSpace(query)

	if strings.Contains(query, "=>[KNN ") {
		start := strings.Index(query, "=>[KNN ")
		if start < 0 || !strings.HasSuffix(query, "]") {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		base := strings.TrimSpace(query[:start])
		if base != "(*)" && base != "*" {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported vector prefilter")
		}
		body := strings.TrimSuffix(query[start+len("=>["):], "]")
		parts := strings.Fields(body)
		if len(parts) < 4 || strings.ToUpper(parts[0]) != "KNN" {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		k, err := strconv.Atoi(parts[1])
		if err != nil || k < 0 {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		if !strings.HasPrefix(parts[2], "@") || !strings.HasPrefix(parts[3], "$") {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		alias := strings.TrimPrefix(parts[2], "@")
		field, ok := findVectorField(def, alias)
		if !ok {
			offset := strings.Index(query, alias)
			if offset < 0 {
				offset = 0
			}
			return vectorSearchSpec{}, true, fmt.Errorf("SEARCH_SYNTAX Unknown field at offset %d near %s", offset, alias)
		}
		scoreAlias := "__"+alias+"_score"
		if len(parts) >= 6 && strings.EqualFold(parts[4], "AS") {
			scoreAlias = parts[5]
		}
		return vectorSearchSpec{
			field: field, k: k, paramName: strings.TrimPrefix(parts[3], "$"), scoreAlias: scoreAlias,
		}, true, nil
	}

	if strings.HasPrefix(query, "@") && strings.Contains(query, ":[VECTOR_RANGE ") && strings.HasSuffix(query, "]") {
		colon := strings.Index(query, ":[VECTOR_RANGE ")
		alias := query[1:colon]
		field, ok := findVectorField(def, alias)
		if !ok {
			offset := strings.Index(query, alias)
			if offset < 0 { offset = 0 }
			return vectorSearchSpec{}, true, fmt.Errorf("SEARCH_SYNTAX Unknown field at offset %d near %s", offset, alias)
		}
		body := strings.TrimSuffix(query[colon+2:], "]")
		parts := strings.Fields(body)
		if len(parts) != 3 || strings.ToUpper(parts[0]) != "VECTOR_RANGE" || !strings.HasPrefix(parts[2], "$") {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		maxDistance, err := strconv.ParseFloat(parts[1], 64)
		if err != nil || maxDistance < 0 || math.IsNaN(maxDistance) || math.IsInf(maxDistance, 0) {
			return vectorSearchSpec{}, true, errors.New("ERR unsupported search query")
		}
		return vectorSearchSpec{
			field: field, rangeMax: &maxDistance, paramName: strings.TrimPrefix(parts[2], "$"),
			scoreAlias: "__"+alias+"_score",
		}, true, nil
	}

	return vectorSearchSpec{}, false, nil
}

func parseVectorSearchOptions(args [][]byte) (vectorSearchOptions, error) {
	options := vectorSearchOptions{params: make(map[string][]byte), dialect: 1}
	for pos := 3; pos < len(args); {
		switch strings.ToUpper(string(args[pos])) {
		case "PARAMS":
			if pos+1 >= len(args) {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			count, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || count < 0 || count%2 != 0 || len(args)-(pos+2) < count {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			pos += 2
			for i := 0; i < count; i += 2 {
				options.params[string(args[pos])] = append([]byte(nil), args[pos+1]...)
				pos += 2
			}
		case "NOCONTENT":
			options.noContent = true
			pos++
		case "SORTBY":
			if pos+1 >= len(args) {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			options.sortBy = string(args[pos+1])
			pos += 2
			if pos < len(args) {
				switch strings.ToUpper(string(args[pos])) {
				case "ASC":
					pos++
				case "DESC":
					options.sortDesc = true
					pos++
				}
			}
		case "RETURN":
			if pos+1 >= len(args) {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			count, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || count < 0 || len(args)-(pos+2) < count {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			pos += 2
			for i := 0; i < count; i++ {
				options.returnFields = append(options.returnFields, string(args[pos]))
				pos++
			}
		case "DIALECT":
			if pos+1 >= len(args) {
				return vectorSearchOptions{}, errors.New("ERR syntax error")
			}
			d, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || (d != 1 && d != 2) {
				return vectorSearchOptions{}, errors.New("ERR unsupported search dialect")
			}
			options.dialect = d
			pos += 2
		default:
			return vectorSearchOptions{}, fmt.Errorf("SEARCH_ARG_UNRECOGNIZED Unknown argument `%s` at position %d for <main>", string(args[pos]), pos-2)
		}
	}
	return options, nil
}

func decodeFloat32Vector(blob []byte, dim int) ([]float32, error) {
	expected := dim * 4
	if len(blob) != expected {
		return nil, fmt.Errorf("SEARCH_QUERY_BAD Error parsing vector similarity query: query vector blob size (%d) does not match index's expected size (%d).", len(blob), expected)
	}
	out := make([]float32, dim)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out, nil
}

func jsonFloat32Vector(raw []byte, dim int) ([]float32, bool) {
	var values []float64
	if err := json.Unmarshal(raw, &values); err != nil || len(values) != dim {
		return nil, false
	}
	out := make([]float32, dim)
	for i, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
		out[i] = float32(value)
	}
	return out, true
}

func cosineVectorDistance(a, b []float32) float64 {
	var dot, aa, bb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x*y
		aa += x*x
		bb += y*y
	}
	if aa == 0 || bb == 0 {
		return 1
	}
	d := 1 - dot/(math.Sqrt(aa)*math.Sqrt(bb))
	if d < 0 && d > -1e-12 {
		return 0
	}
	return d
}

func executeFTVectorSearch(store *engine.Store, args [][]byte, def engine.SearchDefinition, spec vectorSearchSpec) ([]byte, error) {
	options, err := parseVectorSearchOptions(args)
	if err != nil {
		return nil, err
	}
	blob, ok := options.params[spec.paramName]
	if !ok {
		return nil, fmt.Errorf("SEARCH_PARAM_NOT_FOUND Parameter not found `%s`", spec.paramName)
	}
	queryVector, err := decodeFloat32Vector(blob, spec.field.VectorDim)
	if err != nil {
		return nil, err
	}

	keys, ok := store.SearchAllKeys(string(args[1]))
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + string(args[1]))
	}
	hits := make([]vectorSearchHit, 0, len(keys))
	for _, key := range keys {
		raw, found, projectionErr := store.JSONProjection(key, spec.field.Path)
		if projectionErr != nil || !found {
			continue
		}
		vector, valid := jsonFloat32Vector(raw, spec.field.VectorDim)
		if !valid {
			continue
		}
		distance := cosineVectorDistance(vector, queryVector)
		if spec.rangeMax != nil && distance > *spec.rangeMax {
			continue
		}
		hits = append(hits, vectorSearchHit{key: key, distance: distance})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].distance == hits[j].distance {
			return hits[i].key < hits[j].key
		}
		return hits[i].distance < hits[j].distance
	})

	if spec.rangeMax == nil {
		if spec.k < len(hits) {
			hits = hits[:spec.k]
		}
		if spec.k == 0 {
			hits = hits[:0]
		}
	}
	if options.sortBy == spec.scoreAlias && options.sortDesc {
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].distance == hits[j].distance {
				return hits[i].key < hits[j].key
			}
			return hits[i].distance > hits[j].distance
		})
	}

	items := make([][]byte, 0, 1+len(hits)*2)
	items = append(items, integer(int64(len(hits))))
	for _, hit := range hits {
		items = append(items, formatBulkString([]byte(hit.key)))
		if options.noContent {
			continue
		}

		fields := make([][]byte, 0)
		scoreRequested := false
		for _, name := range options.returnFields {
			if name == spec.scoreAlias {
				scoreRequested = true
				break
			}
		}
		if scoreRequested {
			fields = append(fields,
				formatBulkString([]byte(spec.scoreAlias)),
				formatBulkString([]byte(strconv.FormatFloat(hit.distance, 'g', -1, 64))),
			)
		}
		for _, name := range options.returnFields {
			if name == spec.scoreAlias {
				continue
			}
			var path string
			for _, field := range def.Fields {
				if field.Alias == name {
					path = field.Path
					break
				}
			}
			if path == "" && strings.HasPrefix(name, "$") {
				path = name
			}
			if path == "" {
				continue
			}
			raw, found, projectionErr := store.JSONProjection(hit.key, path)
			if projectionErr != nil {
				return nil, projectionErr
			}
			if !found {
				continue
			}
			text := string(raw)
			if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
				if unquoted, unquoteErr := strconv.Unquote(text); unquoteErr == nil {
					text = unquoted
				}
			}
			fields = append(fields, formatBulkString([]byte(name)), formatBulkString([]byte(text)))
		}
		if len(options.returnFields) == 0 {
			raw, found, getErr := store.JSONGet(hit.key, ".")
			if getErr != nil {
				return nil, getErr
			}
			if found {
				fields = append(fields,
					formatBulkString([]byte("$")),
					formatBulkString(raw),
				)
			}
		}
		items = append(items, array(fields...))
	}
	return array(items...), nil
}
