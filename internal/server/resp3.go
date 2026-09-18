package server

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func resp3Null() []byte {
	return []byte("_\r\n")
}

func resp3Map(items ...[]byte) []byte {
	if len(items)%2 != 0 {
		panic("resp3Map requires key/value pairs")
	}

	out := []byte(fmt.Sprintf("%%%d\r\n", len(items)/2))

	for _, item := range items {
		out = append(out, item...)
	}

	return out
}

func helloReply(
	protocol int,
	clientID uint64,
) []byte {
	items := [][]byte{
		formatBulkString([]byte("server")),
		formatBulkString([]byte("snugkv")),

		formatBulkString([]byte("version")),
		formatBulkString([]byte("0.1.0")),

		formatBulkString([]byte("proto")),
		integer(int64(protocol)),

		formatBulkString([]byte("id")),
		integer(int64(clientID)),

		formatBulkString([]byte("mode")),
		formatBulkString([]byte("standalone")),

		formatBulkString([]byte("role")),
		formatBulkString([]byte("master")),

		formatBulkString([]byte("modules")),
		array(),
	}

	if protocol == 3 {
		return resp3Map(items...)
	}

	return array(items...)
}

func validateHelloClientName(name string) error {
	if strings.ContainsAny(name, " \t\r\n") {
		return errors.New(
			"ERR Client names cannot contain spaces, newlines or special characters.",
		)
	}

	return nil
}

func (s *TCPServer) executeHelloConnectionCommand(
	client *clientSession,
	auth *authSession,
	args [][]byte,
) ([]byte, error) {
	if len(args) == 0 ||
		!strings.EqualFold(string(args[0]), "HELLO") {
		return nil, errors.New("ERR internal HELLO dispatch")
	}

	protocol := client.protocolVersion()

	if len(args) >= 2 {
		parsed, err := strconv.Atoi(string(args[1]))
		if err != nil {
			return nil, errors.New(
				"ERR Protocol version is not an integer or out of range",
			)
		}

		if parsed != 2 && parsed != 3 {
			return nil, errors.New(
				"NOPROTO unsupported protocol version",
			)
		}

		protocol = parsed
	}

	var (
		authUser string
		authPass string
		doAuth   bool

		setName string
		doName  bool
	)

	for i := 2; i < len(args); {
		option := strings.ToUpper(string(args[i]))

		switch option {
		case "AUTH":
			if i+2 >= len(args) {
				return nil, fmt.Errorf(
					"ERR Syntax error in HELLO option '%s'",
					string(args[i]),
				)
			}

			authUser = string(args[i+1])
			authPass = string(args[i+2])
			doAuth = true
			i += 3

		case "SETNAME":
			if i+1 >= len(args) {
				return nil, fmt.Errorf(
					"ERR Syntax error in HELLO option '%s'",
					string(args[i]),
				)
			}

			setName = string(args[i+1])

			if err := validateHelloClientName(setName); err != nil {
				return nil, err
			}

			doName = true
			i += 2

		default:
			return nil, fmt.Errorf(
				"ERR Syntax error in HELLO option '%s'",
				string(args[i]),
			)
		}
	}

	if doAuth {
		_, err := s.server.executeAUTH(
			auth,
			[][]byte{
				[]byte("AUTH"),
				[]byte(authUser),
				[]byte(authPass),
			},
		)
		if err != nil {
			return nil, err
		}
	}

	if doName {
		client.setName(setName)
	}

	client.setProtocol(protocol)

	return helloReply(
		protocol,
		client.id,
	), nil
}

// resp3AdaptBasic converts RESP2 nulls into RESP3 nulls while preserving
// all other RESP2 types. This is intentionally the phase-1 adapter.
//
// Command-specific RESP3 shapes such as maps, sets, doubles, verbatim strings,
// and push frames are handled separately in later RESP3 compatibility work.
func resp3AdaptBasic(data []byte) []byte {
	pos := 0

	out, ok := resp3AdaptValue(
		data,
		&pos,
	)

	if !ok || pos != len(data) {
		return data
	}

	return out
}

func resp3AdaptValue(
	data []byte,
	pos *int,
) ([]byte, bool) {
	if *pos >= len(data) {
		return nil, false
	}

	start := *pos
	marker := data[*pos]
	*pos = *pos + 1

	switch marker {
	case '+', '-', ':':
		if !consumeRESPLine(data, pos) {
			return nil, false
		}

		return append(
			[]byte(nil),
			data[start:*pos]...,
		), true

	case '$':
		lineStart := *pos

		if !consumeRESPLine(data, pos) {
			return nil, false
		}

		rawLength := data[lineStart : *pos-2]

		n, err := strconv.Atoi(string(rawLength))
		if err != nil {
			return nil, false
		}

		if n == -1 {
			return resp3Null(), true
		}

		if n < 0 ||
			*pos+n+2 > len(data) ||
			!bytes.Equal(
				data[*pos+n:*pos+n+2],
				[]byte("\r\n"),
			) {
			return nil, false
		}

		*pos += n + 2

		return append(
			[]byte(nil),
			data[start:*pos]...,
		), true

	case '*':
		lineStart := *pos

		if !consumeRESPLine(data, pos) {
			return nil, false
		}

		rawLength := data[lineStart : *pos-2]

		n, err := strconv.Atoi(string(rawLength))
		if err != nil {
			return nil, false
		}

		if n == -1 {
			return resp3Null(), true
		}

		if n < 0 {
			return nil, false
		}

		out := append(
			[]byte(nil),
			data[start:*pos]...,
		)

		for i := 0; i < n; i++ {
			item, ok := resp3AdaptValue(
				data,
				pos,
			)
			if !ok {
				return nil, false
			}

			out = append(out, item...)
		}

		return out, true

	default:
		// Already RESP3 or a type this phase does not transform.
		return append(
			[]byte(nil),
			data[start:]...,
		), true
	}
}

func consumeRESPLine(
	data []byte,
	pos *int,
) bool {
	for *pos+1 < len(data) {
		if data[*pos] == '\r' &&
			data[*pos+1] == '\n' {
			*pos += 2
			return true
		}

		*pos = *pos + 1
	}

	return false
}

func resp3AdaptCommand(
	args [][]byte,
	data []byte,
) []byte {
	if len(args) == 0 {
		return resp3AdaptBasic(data)
	}

	command := strings.ToUpper(string(args[0]))

	switch command {
	case "ACL":
		if len(args) >= 2 &&
			strings.EqualFold(
				string(args[1]),
				"GETUSER",
			) {
			if out, ok :=
				resp3ACLGetUser(data); ok {
				return out
			}
		}

	case "COMMAND":
		if len(args) >= 2 &&
			strings.EqualFold(
				string(args[1]),
				"INFO",
			) {
			if out, ok :=
				resp3CommandInfo(data); ok {
				return out
			}
		}

	case "ZSCORE", "ZINCRBY":
		if out, ok :=
			resp3DoubleFromBulk(data); ok {
			return out
		}

	case "ZMSCORE":
		if out, ok :=
			resp3ArrayBulkDoubles(data); ok {
			return out
		}

	case "ZRANDMEMBER":
		if hasRESPCommandArgument(
			args[2:],
			"WITHSCORES",
		) {
			if out, ok :=
				resp3MemberScorePairs(data); ok {
				return out
			}
		}

	case "ZPOPMIN", "ZPOPMAX":
		if out, ok :=
			resp3MemberScorePairs(data); ok {
			return out
		}

	case "GEOPOS":
		if out, ok :=
			resp3GeoPos(data); ok {
			return out
		}

	case "GEOSEARCH":
		if out, ok :=
			resp3GeoSearch(
				args[2:],
				data,
			); ok {
			return out
		}

	case "XREAD", "XREADGROUP":
		if out, ok :=
			resp3StreamReadMap(data); ok {
			return out
		}

	case "XINFO":
		if len(args) >= 2 {
			switch strings.ToUpper(
				string(args[1]),
			) {
			case "STREAM":
				if out, ok :=
					resp3FlatArrayMap(
						data,
					); ok {
					return out
				}

			case "GROUPS", "CONSUMERS":
				if out, ok :=
					resp3ArrayOfFlatMaps(
						data,
					); ok {
					return out
				}
			}
		}

	case "FUNCTION":
		if len(args) >= 2 &&
			strings.EqualFold(
				string(args[1]),
				"STATS",
			) {
			if out, ok :=
				resp3FunctionStats(data); ok {
				return out
			}
		}

	case "CLIENT":
		if len(args) >= 2 &&
			strings.EqualFold(
				string(args[1]),
				"INFO",
			) {
			if payload, ok :=
				resp2BulkPayload(data); ok {
				return resp3Verbatim(
					"txt",
					payload,
				)
			}
		}

	case "CONFIG":
		if len(args) >= 2 &&
			strings.EqualFold(
				string(args[1]),
				"GET",
			) {
			if out, ok :=
				resp3MapFromFlatArray(
					data,
				); ok {
				return out
			}
		}

	case "HGETALL":
		if out, ok := resp3MapFromFlatArray(data); ok {
			return out
		}

	case "SMEMBERS":
		if out, ok := resp3SetFromArray(data); ok {
			return out
		}

	case "ZRANGE":
		if hasRESPCommandArgument(
			args[2:],
			"WITHSCORES",
		) {
			if out, ok := resp3ZSetRangeWithScores(data); ok {
				return out
			}
		}

	case "INFO":
		if payload, ok := resp2BulkPayload(data); ok {
			return resp3Verbatim(
				"txt",
				payload,
			)
		}
	}

	return resp3AdaptBasic(data)
}

func hasRESPCommandArgument(
	args [][]byte,
	expected string,
) bool {
	for _, arg := range args {
		if strings.EqualFold(
			string(arg),
			expected,
		) {
			return true
		}
	}

	return false
}

func resp3MapFromFlatArray(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok || len(items)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(items)/2,
		),
	)

	for _, item := range items {
		out = append(
			out,
			resp3AdaptBasic(item)...,
		)
	}

	return out, true
}

func resp3SetFromArray(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"~%d\r\n",
			len(items),
		),
	)

	for _, item := range items {
		out = append(
			out,
			resp3AdaptBasic(item)...,
		)
	}

	return out, true
}

func resp3ZSetRangeWithScores(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok || len(items)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(items)/2,
		),
	)

	for i := 0; i < len(items); i += 2 {
		score, ok := resp2BulkPayload(
			items[i+1],
		)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			'*', '2', '\r', '\n',
		)

		out = append(
			out,
			resp3AdaptBasic(items[i])...,
		)

		out = append(
			out,
			',',
		)

		out = append(
			out,
			score...,
		)

		out = append(
			out,
			'\r',
			'\n',
		)
	}

	return out, true
}

func resp3Verbatim(
	format string,
	payload []byte,
) []byte {
	body := make(
		[]byte,
		0,
		len(format)+1+len(payload),
	)

	body = append(body, format...)
	body = append(body, ':')
	body = append(body, payload...)

	out := []byte(
		fmt.Sprintf(
			"=%d\r\n",
			len(body),
		),
	)

	out = append(out, body...)
	out = append(out, '\r', '\n')

	return out
}

func resp2BulkPayload(
	data []byte,
) ([]byte, bool) {
	if len(data) < 4 || data[0] != '$' {
		return nil, false
	}

	lineEnd := bytes.Index(
		data,
		[]byte("\r\n"),
	)
	if lineEnd < 0 {
		return nil, false
	}

	length, err := strconv.Atoi(
		string(data[1:lineEnd]),
	)
	if err != nil || length < 0 {
		return nil, false
	}

	start := lineEnd + 2
	end := start + length

	if end+2 != len(data) {
		return nil, false
	}

	if data[end] != '\r' ||
		data[end+1] != '\n' {
		return nil, false
	}

	return append(
		[]byte(nil),
		data[start:end]...,
	), true
}

func resp2ArrayElements(
	data []byte,
) ([][]byte, bool) {
	if len(data) < 4 || data[0] != '*' {
		return nil, false
	}

	lineEnd := bytes.Index(
		data,
		[]byte("\r\n"),
	)
	if lineEnd < 0 {
		return nil, false
	}

	count, err := strconv.Atoi(
		string(data[1:lineEnd]),
	)
	if err != nil || count < 0 {
		return nil, false
	}

	pos := lineEnd + 2

	items := make(
		[][]byte,
		0,
		count,
	)

	for i := 0; i < count; i++ {
		end, ok := resp2ValueEnd(
			data,
			pos,
		)
		if !ok {
			return nil, false
		}

		items = append(
			items,
			append(
				[]byte(nil),
				data[pos:end]...,
			),
		)

		pos = end
	}

	if pos != len(data) {
		return nil, false
	}

	return items, true
}

func resp2ValueEnd(
	data []byte,
	start int,
) (int, bool) {
	if start >= len(data) {
		return 0, false
	}

	marker := data[start]

	lineRelative := bytes.Index(
		data[start:],
		[]byte("\r\n"),
	)
	if lineRelative < 0 {
		return 0, false
	}

	lineEnd := start + lineRelative
	next := lineEnd + 2

	switch marker {
	case '+', '-', ':':
		return next, true

	case '$':
		length, err := strconv.Atoi(
			string(
				data[start+1 : lineEnd],
			),
		)
		if err != nil {
			return 0, false
		}

		if length == -1 {
			return next, true
		}

		if length < 0 {
			return 0, false
		}

		end := next + length

		if end+2 > len(data) {
			return 0, false
		}

		if data[end] != '\r' ||
			data[end+1] != '\n' {
			return 0, false
		}

		return end + 2, true

	case '*':
		count, err := strconv.Atoi(
			string(
				data[start+1 : lineEnd],
			),
		)
		if err != nil {
			return 0, false
		}

		if count == -1 {
			return next, true
		}

		if count < 0 {
			return 0, false
		}

		pos := next

		for i := 0; i < count; i++ {
			end, ok := resp2ValueEnd(
				data,
				pos,
			)
			if !ok {
				return 0, false
			}

			pos = end
		}

		return pos, true
	}

	return 0, false
}

func resp3ACLGetUser(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok || len(items)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(items)/2,
		),
	)

	for i := 0; i < len(items); i += 2 {
		keyPayload, ok := resp2BulkPayload(
			items[i],
		)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			resp3AdaptBasic(items[i])...,
		)

		switch string(keyPayload) {
		case "flags":
			value, ok := resp3SetFromArray(
				items[i+1],
			)
			if !ok {
				return nil, false
			}

			out = append(out, value...)

		case "selectors":
			value, ok :=
				resp3ACLSelectors(
					items[i+1],
				)
			if !ok {
				return nil, false
			}

			out = append(out, value...)

		default:
			out = append(
				out,
				resp3AdaptBasic(
					items[i+1],
				)...,
			)
		}
	}

	return out, true
}

func resp3ACLSelectors(
	data []byte,
) ([]byte, bool) {
	selectors, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(selectors),
		),
	)

	for _, selector := range selectors {
		fields, ok :=
			resp2ArrayElements(selector)
		if !ok ||
			len(fields)%2 != 0 {
			return nil, false
		}

		out = append(
			out,
			[]byte(
				fmt.Sprintf(
					"%%%d\r\n",
					len(fields)/2,
				),
			)...,
		)

		for _, field := range fields {
			out = append(
				out,
				resp3AdaptBasic(field)...,
			)
		}
	}

	return out, true
}

func resp3CommandInfo(
	data []byte,
) ([]byte, bool) {
	entries, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(entries),
		),
	)

	for _, entry := range entries {
		if bytes.Equal(
			entry,
			[]byte("$-1\r\n"),
		) {
			out = append(
				out,
				resp3Null()...,
			)
			continue
		}

		converted, ok :=
			resp3CommandInfoEntry(entry)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			converted...,
		)
	}

	return out, true
}

func resp3CommandInfoEntry(
	data []byte,
) ([]byte, bool) {
	fields, ok := resp2ArrayElements(data)
	if !ok || len(fields) != 10 {
		return nil, false
	}

	out := []byte("*10\r\n")

	for index, field := range fields {
		var converted []byte

		switch index {
		case 2, 6, 7, 9:
			converted, ok =
				resp3SetFromArray(field)

		case 8:
			converted, ok =
				resp3CommandKeySpecs(field)

		default:
			converted =
				resp3AdaptBasic(field)
			ok = true
		}

		if !ok {
			return nil, false
		}

		out = append(
			out,
			converted...,
		)
	}

	return out, true
}

func resp3CommandKeySpecs(
	data []byte,
) ([]byte, bool) {
	specs, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"~%d\r\n",
			len(specs),
		),
	)

	for _, spec := range specs {
		converted, ok :=
			resp3CommandKeySpec(spec)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			converted...,
		)
	}

	return out, true
}

func resp3CommandKeySpec(
	data []byte,
) ([]byte, bool) {
	fields, ok := resp2ArrayElements(data)
	if !ok ||
		len(fields)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(fields)/2,
		),
	)

	for i := 0; i < len(fields); i += 2 {
		key, ok := resp2BulkPayload(
			fields[i],
		)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			resp3AdaptBasic(fields[i])...,
		)

		switch string(key) {
		case "flags":
			value, ok :=
				resp3SetFromArray(
					fields[i+1],
				)
			if !ok {
				return nil, false
			}

			out = append(out, value...)

		case "begin_search", "find_keys":
			value, ok :=
				resp3CommandSpecMap(
					fields[i+1],
				)
			if !ok {
				return nil, false
			}

			out = append(out, value...)

		default:
			out = append(
				out,
				resp3AdaptBasic(
					fields[i+1],
				)...,
			)
		}
	}

	return out, true
}

func resp3CommandSpecMap(
	data []byte,
) ([]byte, bool) {
	fields, ok := resp2ArrayElements(data)
	if !ok ||
		len(fields)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(fields)/2,
		),
	)

	for i := 0; i < len(fields); i += 2 {
		key, ok := resp2BulkPayload(
			fields[i],
		)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			resp3AdaptBasic(fields[i])...,
		)

		if string(key) == "spec" {
			value, ok :=
				resp3FlatArrayMap(
					fields[i+1],
				)
			if !ok {
				return nil, false
			}

			out = append(out, value...)
			continue
		}

		out = append(
			out,
			resp3AdaptBasic(
				fields[i+1],
			)...,
		)
	}

	return out, true
}

func resp3FlatArrayMap(
	data []byte,
) ([]byte, bool) {
	fields, ok := resp2ArrayElements(data)
	if !ok ||
		len(fields)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(fields)/2,
		),
	)

	for _, field := range fields {
		out = append(
			out,
			resp3AdaptBasic(field)...,
		)
	}

	return out, true
}

func resp3PubSubPush(
	data []byte,
) []byte {
	if len(data) == 0 || data[0] != '*' {
		return resp3AdaptBasic(data)
	}

	lineEnd := bytes.Index(
		data,
		[]byte("\r\n"),
	)
	if lineEnd < 0 {
		return resp3AdaptBasic(data)
	}

	count, err := strconv.Atoi(
		string(data[1:lineEnd]),
	)
	if err != nil || count < 0 {
		return resp3AdaptBasic(data)
	}

	pos := lineEnd + 2

	out := []byte(
		fmt.Sprintf(
			">%d\r\n",
			count,
		),
	)

	for i := 0; i < count; i++ {
		end, ok := resp2ValueEnd(
			data,
			pos,
		)
		if !ok {
			return resp3AdaptBasic(data)
		}

		out = append(
			out,
			resp3AdaptBasic(
				data[pos:end],
			)...,
		)

		pos = end
	}

	if pos != len(data) {
		return resp3AdaptBasic(data)
	}

	return out
}

func resp3DoubleFromBulk(
	data []byte,
) ([]byte, bool) {
	payload, ok := resp2BulkPayload(data)
	if !ok {
		return nil, false
	}

	out := make(
		[]byte,
		0,
		len(payload)+3,
	)

	out = append(out, ',')
	out = append(out, payload...)
	out = append(out, '\r', '\n')

	return out, true
}

func resp3ArrayBulkDoubles(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(items),
		),
	)

	for _, item := range items {
		if bytes.Equal(
			item,
			[]byte("$-1\r\n"),
		) {
			out = append(
				out,
				resp3Null()...,
			)
			continue
		}

		value, ok :=
			resp3DoubleFromBulk(item)
		if !ok {
			return nil, false
		}

		out = append(out, value...)
	}

	return out, true
}

func resp3MemberScorePairs(
	data []byte,
) ([]byte, bool) {
	return resp3ZSetRangeWithScores(data)
}

func resp3GeoPos(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(items),
		),
	)

	for _, item := range items {
		if resp2IsNull(item) {
			out = append(
				out,
				resp3Null()...,
			)
			continue
		}

		coords, ok :=
			resp2ArrayElements(item)
		if !ok || len(coords) != 2 {
			return nil, false
		}

		out = append(
			out,
			[]byte("*2\r\n")...,
		)

		for _, coord := range coords {
			value, ok :=
				resp3DoubleFromBulk(coord)
			if !ok {
				return nil, false
			}

			out = append(
				out,
				value...,
			)
		}
	}

	return out, true
}

func resp3GeoSearch(
	args [][]byte,
	data []byte,
) ([]byte, bool) {
	if !hasRESPCommandArgument(
		args,
		"WITHCOORD",
	) {
		return nil, false
	}

	rows, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(rows),
		),
	)

	for _, row := range rows {
		fields, ok :=
			resp2ArrayElements(row)
		if !ok || len(fields) == 0 {
			return nil, false
		}

		out = append(
			out,
			[]byte(
				fmt.Sprintf(
					"*%d\r\n",
					len(fields),
				),
			)...,
		)

		for i, field := range fields {
			if i != len(fields)-1 {
				out = append(
					out,
					resp3AdaptBasic(field)...,
				)
				continue
			}

			coords, ok :=
				resp2ArrayElements(field)
			if !ok || len(coords) != 2 {
				return nil, false
			}

			out = append(
				out,
				[]byte("*2\r\n")...,
			)

			for _, coord := range coords {
				value, ok :=
					resp3DoubleFromBulk(coord)
				if !ok {
					return nil, false
				}

				out = append(
					out,
					value...,
				)
			}
		}
	}

	return out, true
}

func resp3StreamReadMap(
	data []byte,
) ([]byte, bool) {
	streams, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(streams),
		),
	)

	for _, stream := range streams {
		fields, ok :=
			resp2ArrayElements(stream)
		if !ok || len(fields) != 2 {
			return nil, false
		}

		out = append(
			out,
			resp3AdaptBasic(fields[0])...,
		)

		out = append(
			out,
			resp3AdaptBasic(fields[1])...,
		)
	}

	return out, true
}

func resp3ArrayOfFlatMaps(
	data []byte,
) ([]byte, bool) {
	items, ok := resp2ArrayElements(data)
	if !ok {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"*%d\r\n",
			len(items),
		),
	)

	for _, item := range items {
		value, ok :=
			resp3FlatArrayMap(item)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			value...,
		)
	}

	return out, true
}

func resp3FunctionStats(
	data []byte,
) ([]byte, bool) {
	fields, ok := resp2ArrayElements(data)
	if !ok ||
		len(fields)%2 != 0 {
		return nil, false
	}

	out := []byte(
		fmt.Sprintf(
			"%%%d\r\n",
			len(fields)/2,
		),
	)

	for i := 0; i < len(fields); i += 2 {
		key, ok := resp2BulkPayload(
			fields[i],
		)
		if !ok {
			return nil, false
		}

		out = append(
			out,
			resp3AdaptBasic(fields[i])...,
		)

		if string(key) != "engines" {
			out = append(
				out,
				resp3AdaptBasic(
					fields[i+1],
				)...,
			)
			continue
		}

		engines, ok :=
			resp2ArrayElements(fields[i+1])
		if !ok ||
			len(engines)%2 != 0 {
			return nil, false
		}

		out = append(
			out,
			[]byte(
				fmt.Sprintf(
					"%%%d\r\n",
					len(engines)/2,
				),
			)...,
		)

		for j := 0; j < len(engines); j += 2 {
			out = append(
				out,
				resp3AdaptBasic(
					engines[j],
				)...,
			)

			stats, ok :=
				resp3FlatArrayMap(
					engines[j+1],
				)
			if !ok {
				return nil, false
			}

			out = append(
				out,
				stats...,
			)
		}
	}

	return out, true
}

func resp2IsNull(
	data []byte,
) bool {
	return bytes.Equal(
		data,
		[]byte("$-1\r\n"),
	) || bytes.Equal(
		data,
		[]byte("*-1\r\n"),
	)
}
