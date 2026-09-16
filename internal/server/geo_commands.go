package server

import (
	"errors"
	"fmt"
	"math"
	"snugkv/internal/engine"
	"sort"
	"strconv"
	"strings"
)

const (
	geoLongMin       = -180.0
	geoLongMax       = 180.0
	geoLatMin        = -85.05112878
	geoLatMax        = 85.05112878
	geoStep          = 26
	geoEarthRadiusM  = 6372797.560856
	geoStandardLatMin = -90.0
	geoStandardLatMax = 90.0
)

var geoCommands = map[string]commandInfo{
	"GEOADD":         {5, 0, 1, 1, 1, true},
	"GEODIST":        {4, 5, 1, 1, 1, false},
	"GEOHASH":        {3, 0, 1, 1, 1, false},
	"GEOPOS":         {3, 0, 1, 1, 1, false},
	"GEOSEARCH":      {7, 0, 1, 1, 1, false},
	"GEOSEARCHSTORE": {8, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range geoCommands {
		commandTable[name] = info
	}
}

func isGeoCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := geoCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func (s *Server) executeGeo(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := geoCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown GEO command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	switch cmd {
	case "GEOADD":
		return s.executeGeoAdd(args)
	case "GEODIST":
		return s.executeGeoDist(args)
	case "GEOHASH":
		return s.executeGeoHash(args)
	case "GEOPOS":
		return s.executeGeoPos(args)
	case "GEOSEARCH":
		return s.executeGeoSearch(args, false)
	case "GEOSEARCHSTORE":
		return s.executeGeoSearch(args, true)
	default:
		return nil, errors.New("ERR unknown GEO command")
	}
}

func geoSpread32(value uint32) uint64 {
	x := uint64(value)
	x = (x | x<<16) & 0x0000FFFF0000FFFF
	x = (x | x<<8) & 0x00FF00FF00FF00FF
	x = (x | x<<4) & 0x0F0F0F0F0F0F0F0F
	x = (x | x<<2) & 0x3333333333333333
	x = (x | x<<1) & 0x5555555555555555
	return x
}

func geoInterleave(latitude, longitude uint32) uint64 {
	return geoSpread32(latitude) | geoSpread32(longitude)<<1
}

func geoDeinterleave(interleaved uint64) uint64 {
	x := interleaved
	y := interleaved >> 1
	masks := [...]uint64{
		0x5555555555555555,
		0x3333333333333333,
		0x0F0F0F0F0F0F0F0F,
		0x00FF00FF00FF00FF,
		0x0000FFFF0000FFFF,
		0x00000000FFFFFFFF,
	}
	shifts := [...]uint{0, 1, 2, 4, 8, 16}
	for i := range masks {
		x = (x | x>>shifts[i]) & masks[i]
		y = (y | y>>shifts[i]) & masks[i]
	}
	return x | y<<32
}

func geoEncodeBits(longitude, latitude, latMin, latMax float64) uint64 {
	scale := float64(uint64(1) << geoStep)
	latOffset := (latitude - latMin) / (latMax - latMin)
	longOffset := (longitude - geoLongMin) / (geoLongMax - geoLongMin)
	latitudeFixed := uint32(latOffset * scale)
	longitudeFixed := uint32(longOffset * scale)
	return geoInterleave(latitudeFixed, longitudeFixed)
}

func geoValidCoordinates(longitude, latitude float64) bool {
	return !math.IsNaN(longitude) && !math.IsNaN(latitude) &&
		!math.IsInf(longitude, 0) && !math.IsInf(latitude, 0) &&
		longitude >= geoLongMin && longitude <= geoLongMax &&
		latitude >= geoLatMin && latitude <= geoLatMax
}

func geoEncodeScore(longitude, latitude float64) (float64, error) {
	if !geoValidCoordinates(longitude, latitude) {
		return 0, fmt.Errorf("ERR invalid longitude,latitude pair %f,%f", longitude, latitude)
	}
	return float64(geoEncodeBits(longitude, latitude, geoLatMin, geoLatMax)), nil
}

func geoDecodeScore(score float64) (longitude, latitude float64, ok bool) {
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score >= 18446744073709551616.0 {
		return 0, 0, false
	}
	separated := geoDeinterleave(uint64(score))
	latitudeFixed := uint32(separated)
	longitudeFixed := uint32(separated >> 32)
	scale := float64(uint64(1) << geoStep)

	longitudeMin := geoLongMin + float64(longitudeFixed)/scale*(geoLongMax-geoLongMin)
	longitudeMax := geoLongMin + float64(uint64(longitudeFixed)+1)/scale*(geoLongMax-geoLongMin)
	latitudeMin := geoLatMin + float64(latitudeFixed)/scale*(geoLatMax-geoLatMin)
	latitudeMax := geoLatMin + float64(uint64(latitudeFixed)+1)/scale*(geoLatMax-geoLatMin)

	longitude = (longitudeMin + longitudeMax) / 2
	latitude = (latitudeMin + latitudeMax) / 2
	if longitude > geoLongMax {
		longitude = geoLongMax
	}
	if longitude < geoLongMin {
		longitude = geoLongMin
	}
	if latitude > geoLatMax {
		latitude = geoLatMax
	}
	if latitude < geoLatMin {
		latitude = geoLatMin
	}
	return longitude, latitude, true
}

func geoHashString(longitude, latitude float64) string {
	bits := geoEncodeBits(longitude, latitude, geoStandardLatMin, geoStandardLatMax)
	const alphabet = "0123456789bcdefghjkmnpqrstuvwxyz"
	var out [11]byte
	for i := 0; i < 11; i++ {
		index := uint64(0)
		if i < 10 {
			index = (bits >> (52 - uint((i+1)*5))) & 0x1f
		}
		out[i] = alphabet[index]
	}
	return string(out[:])
}

func geoRadians(value float64) float64 { return value * (math.Pi / 180) }

func geoLatitudeDistanceMeters(latitude1, latitude2 float64) float64 {
	return geoEarthRadiusM * math.Abs(geoRadians(latitude2)-geoRadians(latitude1))
}

func geoDistanceMeters(longitude1, latitude1, longitude2, latitude2 float64) float64 {
	longitude1Radians := geoRadians(longitude1)
	longitude2Radians := geoRadians(longitude2)
	v := math.Sin((longitude2Radians - longitude1Radians) / 2)
	if v == 0 {
		return geoLatitudeDistanceMeters(latitude1, latitude2)
	}
	latitude1Radians := geoRadians(latitude1)
	latitude2Radians := geoRadians(latitude2)
	u := math.Sin((latitude2Radians - latitude1Radians) / 2)
	a := u*u + math.Cos(latitude1Radians)*math.Cos(latitude2Radians)*v*v
	if a > 1 {
		a = 1
	}
	return 2 * geoEarthRadiusM * math.Asin(math.Sqrt(a))
}

func geoUnitMeters(unit []byte) (float64, error) {
	switch strings.ToLower(string(unit)) {
	case "m":
		return 1, nil
	case "km":
		return 1000, nil
	case "ft":
		return 0.3048, nil
	case "mi":
		return 1609.34, nil
	default:
		return 0, errors.New("ERR unsupported unit provided. please use M, KM, FT, MI")
	}
}

func geoParseFloat(value []byte, message string) (float64, error) {
	n, err := strconv.ParseFloat(string(value), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, errors.New(message)
	}
	return n, nil
}

func geoCoordinateBulk(value float64) []byte {
	return formatBulkString([]byte(strconv.FormatFloat(value, 'g', -1, 64)))
}

func geoDistanceBulk(value float64) []byte {
	return formatBulkString([]byte(strconv.FormatFloat(value, 'f', 4, 64)))
}

func (s *Server) executeGeoAdd(args [][]byte) ([]byte, error) {
	nx, xx, ch := false, false, false
	index := 2
	for index < len(args) {
		switch strings.ToUpper(string(args[index])) {
		case "NX":
			nx = true
		case "XX":
			xx = true
		case "CH":
			ch = true
		default:
			goto coordinates
		}
		index++
	}

coordinates:
	if nx && xx || index >= len(args) || (len(args)-index)%3 != 0 {
		return nil, errors.New("ERR syntax error")
	}
	pairs := make([]engine.ZSetItem, 0, (len(args)-index)/3)
	for index < len(args) {
		longitude, err := geoParseFloat(args[index], "ERR value is not a valid float")
		if err != nil {
			return nil, err
		}
		latitude, err := geoParseFloat(args[index+1], "ERR value is not a valid float")
		if err != nil {
			return nil, err
		}
		score, err := geoEncodeScore(longitude, latitude)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, engine.ZSetItem{Member: append([]byte(nil), args[index+2]...), Score: score})
		index += 3
	}
	count, _, _, err := s.store.ZSetAdd(string(args[1]), pairs, engine.ZSetAddOptions{NX: nx, XX: xx, CH: ch})
	if err != nil {
		return nil, err
	}
	return integer(count), nil
}

func (s *Server) executeGeoPos(args [][]byte) ([]byte, error) {
	items := make([][]byte, 0, len(args)-2)
	for _, member := range args[2:] {
		score, found, err := s.store.ZSetScore(string(args[1]), member)
		if err != nil {
			return nil, err
		}
		if !found {
			items = append(items, []byte("*-1\r\n"))
			continue
		}
		longitude, latitude, ok := geoDecodeScore(score)
		if !ok {
			items = append(items, []byte("*-1\r\n"))
			continue
		}
		items = append(items, array(geoCoordinateBulk(longitude), geoCoordinateBulk(latitude)))
	}
	return array(items...), nil
}

func (s *Server) executeGeoHash(args [][]byte) ([]byte, error) {
	items := make([][]byte, 0, len(args)-2)
	for _, member := range args[2:] {
		score, found, err := s.store.ZSetScore(string(args[1]), member)
		if err != nil {
			return nil, err
		}
		if !found {
			items = append(items, nullBulk())
			continue
		}
		longitude, latitude, ok := geoDecodeScore(score)
		if !ok {
			items = append(items, nullBulk())
			continue
		}
		items = append(items, formatBulkString([]byte(geoHashString(longitude, latitude))))
	}
	return array(items...), nil
}

func (s *Server) executeGeoDist(args [][]byte) ([]byte, error) {
	unitMeters := 1.0
	if len(args) == 5 {
		var err error
		unitMeters, err = geoUnitMeters(args[4])
		if err != nil {
			return nil, err
		}
	}
	score1, found, err := s.store.ZSetScore(string(args[1]), args[2])
	if err != nil {
		return nil, err
	}
	if !found {
		return nullBulk(), nil
	}
	score2, found, err := s.store.ZSetScore(string(args[1]), args[3])
	if err != nil {
		return nil, err
	}
	if !found {
		return nullBulk(), nil
	}
	longitude1, latitude1, ok1 := geoDecodeScore(score1)
	longitude2, latitude2, ok2 := geoDecodeScore(score2)
	if !ok1 || !ok2 {
		return nullBulk(), nil
	}
	distance := geoDistanceMeters(longitude1, latitude1, longitude2, latitude2) / unitMeters
	return geoDistanceBulk(distance), nil
}

type geoSearchSpec struct {
	fromMember []byte
	longitude  float64
	latitude   float64
	hasMember  bool
	hasCoords  bool
	byRadius   bool
	byBox      bool
	radius     float64
	width      float64
	height     float64
	unitMeters float64
	withDist   bool
	withHash   bool
	withCoord  bool
	sortOrder  int
	count      int64
	any        bool
	storeDist  bool
}

type geoSearchResult struct {
	item      engine.ZSetItem
	longitude float64
	latitude  float64
	distanceM float64
}

func parseGeoSearchSpec(args [][]byte, start int, store bool) (geoSearchSpec, error) {
	spec := geoSearchSpec{unitMeters: 1}
	for i := start; i < len(args); {
		option := strings.ToUpper(string(args[i]))
		switch option {
		case "FROMMEMBER":
			if spec.hasCoords || i+1 >= len(args) {
				return spec, errors.New("ERR syntax error")
			}
			spec.hasMember = true
			spec.fromMember = append(spec.fromMember[:0], args[i+1]...)
			i += 2
		case "FROMLONLAT":
			if spec.hasMember || i+2 >= len(args) {
				return spec, errors.New("ERR syntax error")
			}
			longitude, err := geoParseFloat(args[i+1], "ERR value is not a valid float")
			if err != nil {
				return spec, err
			}
			latitude, err := geoParseFloat(args[i+2], "ERR value is not a valid float")
			if err != nil {
				return spec, err
			}
			if !geoValidCoordinates(longitude, latitude) {
				return spec, fmt.Errorf("ERR invalid longitude,latitude pair %f,%f", longitude, latitude)
			}
			spec.hasCoords = true
			spec.longitude, spec.latitude = longitude, latitude
			i += 3
		case "BYRADIUS":
			if spec.byBox || i+2 >= len(args) {
				return spec, errors.New("ERR syntax error")
			}
			radius, err := geoParseFloat(args[i+1], "ERR need numeric radius")
			if err != nil {
				return spec, err
			}
			if radius < 0 {
				return spec, errors.New("ERR radius cannot be negative")
			}
			unitMeters, err := geoUnitMeters(args[i+2])
			if err != nil {
				return spec, err
			}
			spec.byRadius, spec.radius, spec.unitMeters = true, radius, unitMeters
			i += 3
		case "BYBOX":
			if spec.byRadius || i+3 >= len(args) {
				return spec, errors.New("ERR syntax error")
			}
			width, err := geoParseFloat(args[i+1], "ERR need numeric width")
			if err != nil {
				return spec, err
			}
			height, err := geoParseFloat(args[i+2], "ERR need numeric height")
			if err != nil {
				return spec, err
			}
			if width < 0 || height < 0 {
				return spec, errors.New("ERR height or width cannot be negative")
			}
			unitMeters, err := geoUnitMeters(args[i+3])
			if err != nil {
				return spec, err
			}
			spec.byBox, spec.width, spec.height, spec.unitMeters = true, width, height, unitMeters
			i += 4
		case "ASC":
			spec.sortOrder = 1
			i++
		case "DESC":
			spec.sortOrder = -1
			i++
		case "COUNT":
			if i+1 >= len(args) {
				return spec, errors.New("ERR syntax error")
			}
			count, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil {
				return spec, errors.New("ERR value is not an integer or out of range")
			}
			if count <= 0 {
				return spec, errors.New("ERR COUNT must be > 0")
			}
			spec.count = count
			i += 2
		case "ANY":
			spec.any = true
			i++
		case "WITHDIST":
			if store {
				return spec, errors.New("ERR syntax error")
			}
			spec.withDist = true
			i++
		case "WITHHASH":
			if store {
				return spec, errors.New("ERR syntax error")
			}
			spec.withHash = true
			i++
		case "WITHCOORD":
			if store {
				return spec, errors.New("ERR syntax error")
			}
			spec.withCoord = true
			i++
		case "STOREDIST":
			if !store {
				return spec, errors.New("ERR syntax error")
			}
			spec.storeDist = true
			i++
		default:
			return spec, errors.New("ERR syntax error")
		}
	}

	if spec.hasMember == spec.hasCoords {
		return spec, errors.New("ERR exactly one of FROMMEMBER or FROMLONLAT can be specified")
	}
	if spec.byRadius == spec.byBox {
		return spec, errors.New("ERR exactly one of BYRADIUS and BYBOX can be specified")
	}
	if spec.any && spec.count == 0 {
		return spec, errors.New("ERR the ANY argument requires COUNT argument")
	}
	if spec.count > 0 && spec.sortOrder == 0 && !spec.any {
		spec.sortOrder = 1
	}
	return spec, nil
}

func geoMemberIndex(items []engine.ZSetItem, member []byte) int {
	needle := string(member)
	for i := range items {
		if string(items[i].Member) == needle {
			return i
		}
	}
	return -1
}

func geoMatchesSearch(spec geoSearchSpec, longitude, latitude float64) (float64, bool) {
	distance := geoDistanceMeters(spec.longitude, spec.latitude, longitude, latitude)
	if spec.byRadius {
		return distance, distance <= spec.radius*spec.unitMeters
	}
	if geoLatitudeDistanceMeters(latitude, spec.latitude) > spec.height*spec.unitMeters/2 {
		return distance, false
	}
	longitudeDistance := geoDistanceMeters(longitude, latitude, spec.longitude, latitude)
	return distance, longitudeDistance <= spec.width*spec.unitMeters/2
}

func (s *Server) executeGeoSearch(args [][]byte, store bool) ([]byte, error) {
	sourceIndex, optionsStart := 1, 2
	destination := ""
	if store {
		destination = string(args[1])
		sourceIndex, optionsStart = 2, 3
	}
	spec, err := parseGeoSearchSpec(args, optionsStart, store)
	if err != nil {
		return nil, err
	}

	items, err := s.store.ZSetRange(string(args[sourceIndex]), 0, -1, false)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if store {
			if err := s.store.ZSetReplace(destination, nil); err != nil {
				return nil, err
			}
			return integer(0), nil
		}
		return array(), nil
	}

	if spec.hasMember {
		index := geoMemberIndex(items, spec.fromMember)
		if index < 0 {
			return nil, errors.New("ERR could not decode requested zset member")
		}
		longitude, latitude, ok := geoDecodeScore(items[index].Score)
		if !ok {
			return nil, errors.New("ERR could not decode requested zset member")
		}
		spec.longitude, spec.latitude = longitude, latitude
	}

	results := make([]geoSearchResult, 0)
	for _, item := range items {
		longitude, latitude, ok := geoDecodeScore(item.Score)
		if !ok {
			continue
		}
		distance, matches := geoMatchesSearch(spec, longitude, latitude)
		if !matches {
			continue
		}
		results = append(results, geoSearchResult{item: item, longitude: longitude, latitude: latitude, distanceM: distance})
		if spec.any && spec.count > 0 && int64(len(results)) >= spec.count {
			break
		}
	}

	if spec.sortOrder != 0 {
		sort.SliceStable(results, func(i, j int) bool {
			if spec.sortOrder > 0 {
				return results[i].distanceM < results[j].distanceM
			}
			return results[i].distanceM > results[j].distanceM
		})
	}
	if spec.count > 0 && int64(len(results)) > spec.count {
		results = results[:spec.count]
	}

	if store {
		stored := make([]engine.ZSetItem, len(results))
		for i, result := range results {
			score := result.item.Score
			if spec.storeDist {
				score = result.distanceM / spec.unitMeters
			}
			stored[i] = engine.ZSetItem{Member: append([]byte(nil), result.item.Member...), Score: score}
		}
		if err := s.store.ZSetReplace(destination, stored); err != nil {
			return nil, err
		}
		return integer(int64(len(stored))), nil
	}

	responseItems := make([][]byte, 0, len(results))
	hasOptions := spec.withDist || spec.withHash || spec.withCoord
	for _, result := range results {
		member := formatBulkString(result.item.Member)
		if !hasOptions {
			responseItems = append(responseItems, member)
			continue
		}
		row := make([][]byte, 0, 4)
		row = append(row, member)
		if spec.withDist {
			row = append(row, geoDistanceBulk(result.distanceM/spec.unitMeters))
		}
		if spec.withHash {
			row = append(row, integer(int64(result.item.Score)))
		}
		if spec.withCoord {
			row = append(row, array(geoCoordinateBulk(result.longitude), geoCoordinateBulk(result.latitude)))
		}
		responseItems = append(responseItems, array(row...))
	}
	return array(responseItems...), nil
}
