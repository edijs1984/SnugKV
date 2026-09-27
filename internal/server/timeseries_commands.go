package server

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/engine"
)

var timeSeriesCommands = map[string]commandInfo{
	"TS.CREATE":   {2, 0, 1, 1, 1, true},
	"TS.ADD":      {4, 0, 1, 1, 1, true},
	"TS.GET":      {2, 2, 1, 1, 1, false},
	"TS.RANGE":    {4, 4, 1, 1, 1, false},
	"TS.REVRANGE": {4, 4, 1, 1, 1, false},
	"TS.INCRBY":   {3, 0, 1, 1, 1, true},
	"TS.DECRBY":   {3, 0, 1, 1, 1, true},
	"TS.DEL":      {4, 4, 1, 1, 1, true},
	"TS.INFO":       {2, 2, 1, 1, 1, false},
	"TS.ALTER":      {2, 0, 1, 1, 1, true},
	"TS.MADD":       {4, 0, 1, -1, 3, true},
	"TS.QUERYINDEX": {2, 0, 0, 0, 0, false},
	"TS.MGET":       {3, 0, 0, 0, 0, false},
	"TS.MRANGE":     {4, 0, 0, 0, 0, false},
	"TS.MREVRANGE":  {4, 0, 0, 0, 0, false},
	"TS.CREATERULE": {6, 7, 1, 2, 1, true},
	"TS.DELETERULE": {3, 3, 1, 2, 1, true},
}

func init() {
	for name, info := range timeSeriesCommands {
		commandTable[name] = info
	}
}

func isTimeSeriesCommand(args [][]byte) bool {
	if len(args) == 0 { return false }
	_, ok := timeSeriesCommands[strings.ToUpper(string(args[0]))]
	return ok
}

type timeSeriesCreateOptions struct {
	retention int64
	policy engine.TimeSeriesDuplicatePolicy
	labels []engine.TimeSeriesLabel
}

func parseTimeSeriesPolicy(raw []byte) (engine.TimeSeriesDuplicatePolicy,error) {
	switch strings.ToUpper(string(raw)) {
	case "BLOCK":
		return engine.TimeSeriesBlock,nil
	case "FIRST":
		return engine.TimeSeriesFirst,nil
	case "LAST":
		return engine.TimeSeriesLast,nil
	case "MIN":
		return engine.TimeSeriesMin,nil
	case "MAX":
		return engine.TimeSeriesMax,nil
	case "SUM":
		return engine.TimeSeriesSum,nil
	default:
		return 0,errors.New("ERR TSDB: Unknown DUPLICATE_POLICY")
	}
}

func parseTimeSeriesCreateOptions(args [][]byte, start int) (timeSeriesCreateOptions,error) {
	out:=timeSeriesCreateOptions{policy:engine.TimeSeriesBlock}
	for i:=start;i<len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "RETENTION":
			if i+1>=len(args){return out,errors.New("ERR wrong number of arguments")}
			v,err:=strconv.ParseInt(string(args[i+1]),10,64)
			if err!=nil || v<0{return out,errors.New("TSDB: Couldn't parse RETENTION")}
			out.retention=v
			i+=2
		case "DUPLICATE_POLICY":
			if i+1>=len(args){return out,errors.New("ERR wrong number of arguments")}
			p,err:=parseTimeSeriesPolicy(args[i+1]);if err!=nil{return out,err}
			out.policy=p
			i+=2
		case "LABELS":
			i++
			if (len(args)-i)%2!=0{return out,errors.New("ERR wrong number of arguments")}
			for i<len(args){
				out.labels=append(out.labels,engine.TimeSeriesLabel{Key:string(args[i]),Value:string(args[i+1])})
				i+=2
			}
		default:
			return out,errors.New("ERR TSDB: unknown argument")
		}
	}
	return out,nil
}

func timeSeriesValueString(v float64) []byte {
	if math.IsNaN(v){return []byte("NaN")}
	if math.IsInf(v,1){return []byte("inf")}
	if math.IsInf(v,-1){return []byte("-inf")}
	return formatZSetScore(v)
}

func timeSeriesSampleReply(sample engine.TimeSeriesSample) []byte {
	return array(
		integer(sample.Timestamp),
		[]byte("+"+string(timeSeriesValueString(sample.Value))+"\r\n"),
	)
}

func timeSeriesRangeReply(samples []engine.TimeSeriesSample) []byte {
	items:=make([][]byte,len(samples))
	for i,sample:=range samples{items[i]=timeSeriesSampleReply(sample)}
	return array(items...)
}

func parseTimeSeriesTimestamp(raw []byte) (int64,error) {
	v,err:=strconv.ParseInt(string(raw),10,64)
	if err!=nil{return 0,errors.New("ERR TSDB: invalid timestamp")}
	return v,nil
}

func parseTimeSeriesBound(raw []byte, lower bool)(int64,error){
	if string(raw)=="-" { return -1 << 63,nil }
	if string(raw)=="+" { return 1<<63 - 1,nil }
	return parseTimeSeriesTimestamp(raw)
}

type timeSeriesMultiOptions struct {
	withLabels     bool
	selectedLabels []string
	filters        map[string]string
	filterIndex    int
}

func parseTimeSeriesMultiOptions(args [][]byte, start int) (timeSeriesMultiOptions, error) {
	out := timeSeriesMultiOptions{filters: make(map[string]string)}
	filterIndex := -1

	for i := start; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "WITHLABELS":
			if len(out.selectedLabels) > 0 {
				return out, errors.New("ERR TSDB: cannot accept WITHLABELS and SELECTED_LABELS together")
			}
			out.withLabels = true
			i++
		case "SELECTED_LABELS":
			if out.withLabels {
				return out, errors.New("ERR TSDB: cannot accept WITHLABELS and SELECTED_LABELS together")
			}
			i++
			startLabels := i
			for i < len(args) && !strings.EqualFold(string(args[i]), "FILTER") {
				out.selectedLabels = append(out.selectedLabels, string(args[i]))
				i++
			}
			if i == startLabels {
				return out, errors.New("ERR TSDB: SELECTED_LABELS should have at least 1 parameter")
			}
		case "FILTER":
			filterIndex = i
			i = len(args)
		default:
			// For multi-series commands Redis requires a FILTER section.
			// A bare token encountered while parsing pre-FILTER options is
			// therefore reported as a missing FILTER rather than as a generic
			// unknown option.
			return out, errors.New("ERR TSDB: missing FILTER argument")
		}
	}

	if filterIndex < 0 {
		return out, errors.New("ERR TSDB: missing FILTER argument")
	}
	if filterIndex+1 >= len(args) {
		return out, errors.New("ERR TSDB: missing labels for filter argument")
	}

	for _, raw := range args[filterIndex+1:] {
		filter := string(raw)
		eq := strings.IndexByte(filter, '=')
		if eq <= 0 || eq == len(filter)-1 {
			return out, errors.New("ERR TSDB: invalid filter")
		}
		out.filters[filter[:eq]] = filter[eq+1:]
	}
	out.filterIndex = filterIndex
	return out, nil
}

func timeSeriesLabelsReply(labels []engine.TimeSeriesLabel, opts timeSeriesMultiOptions) []byte {
	if !opts.withLabels && len(opts.selectedLabels) == 0 {
		return array()
	}

	selected := map[string]struct{}{}
	if len(opts.selectedLabels) > 0 {
		for _, label := range opts.selectedLabels {
			selected[label] = struct{}{}
		}
	}

	items := make([][]byte, 0, len(labels))
	for _, label := range labels {
		if len(selected) > 0 {
			if _, ok := selected[label.Key]; !ok {
				continue
			}
		}
		items = append(items, array(
			formatBulkString([]byte(label.Key)),
			formatBulkString([]byte(label.Value)),
		))
	}
	return array(items...)
}

func timeSeriesMultiSeriesReply(
	key string,
	labels []engine.TimeSeriesLabel,
	data []byte,
	opts timeSeriesMultiOptions,
) []byte {
	return array(
		formatBulkString([]byte(key)),
		timeSeriesLabelsReply(labels, opts),
		data,
	)
}

func (s *Server) executeTimeSeries(args [][]byte)([]byte,error){
	if len(args)==0{return nil,errors.New("ERR empty command")}
	cmd:=strings.ToUpper(string(args[0]))
	info,ok:=timeSeriesCommands[cmd]
	if !ok{return nil,errors.New("ERR unknown TimeSeries command")}
	if len(args)<info.min || info.max>0 && len(args)>info.max{
		return nil,errors.New("ERR wrong number of arguments for '"+strings.ToLower(cmd)+"' command")
	}
	key:=string(args[1])

	switch cmd {
	case "TS.CREATE":
		opts,err:=parseTimeSeriesCreateOptions(args,2);if err!=nil{return nil,err}
		if err:=s.store.TimeSeriesCreate(key,opts.retention,opts.policy,opts.labels);err!=nil{return nil,err}
		return []byte("+OK\r\n"),nil

	case "TS.ADD":
		ts,err:=parseTimeSeriesTimestamp(args[2]);if err!=nil{return nil,err}
		value,err:=strconv.ParseFloat(string(args[3]),64)
		if err!=nil{return nil,errors.New("ERR TSDB: invalid value")}
		var createConfig interface{}
		_ = createConfig
		var cfg = (*struct{})(nil)
		_ = cfg
		opts,err:=parseTimeSeriesCreateOptions(args,4);if err!=nil{return nil,err}
		create,err:=engine.NewTimeSeriesConfig(opts.retention,opts.policy,opts.labels)
		if err!=nil{return nil,err}
		_,exists:=s.store.ValueTypeOf(key)
		if exists {
			create=nil
		}
		if err:=s.store.TimeSeriesAdd(key,ts,value,create);err!=nil{return nil,err}
		return integer(ts),nil

	case "TS.GET":
		sample,found,err:=s.store.TimeSeriesGet(key)
		if err!=nil{
			if strings.HasPrefix(err.Error(),"WRONGTYPE "){return nil,errors.New("ERR "+err.Error())}
			return nil,err
		}
		if !found{return nullBulk(),nil}
		return timeSeriesSampleReply(sample),nil

	case "TS.RANGE","TS.REVRANGE":
		from,err:=parseTimeSeriesBound(args[2],true);if err!=nil{return nil,err}
		to,err:=parseTimeSeriesBound(args[3],false);if err!=nil{return nil,err}
		if from>to{return array(),nil}
		samples,err:=s.store.TimeSeriesRange(key,from,to,cmd=="TS.REVRANGE");if err!=nil{return nil,err}
		return timeSeriesRangeReply(samples),nil

	case "TS.DEL":
		from,err:=parseTimeSeriesTimestamp(args[2]);if err!=nil{return nil,err}
		to,err:=parseTimeSeriesTimestamp(args[3]);if err!=nil{return nil,err}
		n,err:=s.store.TimeSeriesDeleteRange(key,from,to);if err!=nil{return nil,err}
		return integer(int64(n)),nil

	case "TS.INCRBY","TS.DECRBY":
		delta,err:=strconv.ParseFloat(string(args[2]),64)
		if err!=nil{return nil,errors.New("ERR TSDB: invalid increase/decrease value")}
		timestamp:=time.Now().UnixMilli()
		optionStart:=3
		if len(args)>=5 && strings.EqualFold(string(args[3]),"TIMESTAMP"){
			if string(args[4])!="*"{
				timestamp,err=parseTimeSeriesTimestamp(args[4]);if err!=nil{return nil,err}
			}
			optionStart=5
		}
		opts,err:=parseTimeSeriesCreateOptions(args,optionStart);if err!=nil{return nil,err}
		create,err:=engine.NewTimeSeriesConfig(opts.retention,opts.policy,opts.labels)
		if err!=nil{return nil,err}
		_,exists:=s.store.ValueTypeOf(key)
		if exists{create=nil}
		if err:=s.store.TimeSeriesIncrBy(key,delta,timestamp,cmd=="TS.DECRBY",create);err!=nil{return nil,err}
		return integer(timestamp),nil

	case "TS.ALTER":
		var (
			retention *int64
			policy    *engine.TimeSeriesDuplicatePolicy
			labels    *[]engine.TimeSeriesLabel
		)
		for i := 2; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "RETENTION":
				if i+1 >= len(args) {
					return nil, errors.New("ERR wrong number of arguments")
				}
				v, err := strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil || v < 0 {
					return nil, errors.New("TSDB: Couldn't parse RETENTION")
				}
				retention = &v
				i += 2
			case "DUPLICATE_POLICY":
				if i+1 >= len(args) {
					return nil, errors.New("ERR wrong number of arguments")
				}
				v, err := parseTimeSeriesPolicy(args[i+1])
				if err != nil {
					return nil, err
				}
				policy = &v
				i += 2
			case "LABELS":
				i++
				if (len(args)-i)%2 != 0 {
					return nil, errors.New("ERR wrong number of arguments")
				}
				next := make([]engine.TimeSeriesLabel, 0, (len(args)-i)/2)
				for i < len(args) {
					next = append(next, engine.TimeSeriesLabel{
						Key: string(args[i]), Value: string(args[i+1]),
					})
					i += 2
				}
				labels = &next
			default:
				return nil, errors.New("ERR TSDB: unknown argument")
			}
		}
		if err := s.store.TimeSeriesAlter(key, retention, policy, labels); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "TS.MADD":
		if (len(args)-1)%3 != 0 {
			return nil, errors.New("ERR wrong number of arguments for 'ts.madd' command")
		}
		items := make([]struct {
			Key       string
			Timestamp int64
			Value     float64
		}, 0, (len(args)-1)/3)
		replies := make([][]byte, 0, (len(args)-1)/3)

		for i := 1; i < len(args); i += 3 {
			ts, err := parseTimeSeriesTimestamp(args[i+1])
			if err != nil {
				return nil, err
			}
			value, err := strconv.ParseFloat(string(args[i+2]), 64)
			if err != nil {
				return nil, errors.New("ERR TSDB: invalid value")
			}
			items = append(items, struct {
				Key       string
				Timestamp int64
				Value     float64
			}{
				Key: string(args[i]), Timestamp: ts, Value: value,
			})
			replies = append(replies, integer(ts))
		}
		if err := s.store.TimeSeriesMAdd(items); err != nil {
			return nil, err
		}
		return array(replies...), nil

	case "TS.QUERYINDEX":
		if len(args) < 2 {
			return nil, errors.New("ERR wrong number of arguments for 'ts.queryindex' command")
		}
		filters := make(map[string]string, len(args)-1)
		for _, raw := range args[1:] {
			filter := string(raw)
			eq := strings.IndexByte(filter, '=')
			if eq <= 0 || eq == len(filter)-1 {
				return nil, errors.New("ERR TSDB: invalid filter")
			}
			filters[filter[:eq]] = filter[eq+1:]
		}
		keys, err := s.store.TimeSeriesQueryIndex(filters)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, len(keys))
		for i, key := range keys {
			items[i] = formatBulkString([]byte(key))
		}
		return array(items...), nil

	case "TS.MGET":
		opts, err := parseTimeSeriesMultiOptions(args, 1)
		if err != nil {
			return nil, err
		}
		keys, err := s.store.TimeSeriesQueryIndex(opts.filters)
		if err != nil {
			return nil, err
		}

		items := make([][]byte, 0, len(keys))
		for _, seriesKey := range keys {
			info, err := s.store.TimeSeriesInfo(seriesKey)
			if err != nil {
				return nil, err
			}
			sample, found, err := s.store.TimeSeriesGet(seriesKey)
			if err != nil {
				return nil, err
			}
			data := nullBulk()
			if found {
				data = timeSeriesSampleReply(sample)
			}
			items = append(items, timeSeriesMultiSeriesReply(seriesKey, info.Labels, data, opts))
		}
		return array(items...), nil

	case "TS.MRANGE", "TS.MREVRANGE":
		from, err := parseTimeSeriesBound(args[1], true)
		if err != nil {
			return nil, err
		}
		to, err := parseTimeSeriesBound(args[2], false)
		if err != nil {
			return nil, err
		}
		if from > to {
			return array(), nil
		}

		opts, err := parseTimeSeriesMultiOptions(args, 3)
		if err != nil {
			return nil, err
		}
		keys, err := s.store.TimeSeriesQueryIndex(opts.filters)
		if err != nil {
			return nil, err
		}

		items := make([][]byte, 0, len(keys))
		for _, seriesKey := range keys {
			info, err := s.store.TimeSeriesInfo(seriesKey)
			if err != nil {
				return nil, err
			}
			samples, err := s.store.TimeSeriesRange(
				seriesKey,
				from,
				to,
				cmd == "TS.MREVRANGE",
			)
			if err != nil {
				return nil, err
			}
			items = append(items, timeSeriesMultiSeriesReply(
				seriesKey,
				info.Labels,
				timeSeriesRangeReply(samples),
				opts,
			))
		}
		return array(items...), nil

	case "TS.CREATERULE":
		if !strings.EqualFold(string(args[3]), "AGGREGATION") {
			return nil, errors.New("ERR TSDB: unknown argument")
		}
		bucketDuration, err := strconv.ParseInt(string(args[5]), 10, 64)
		if err != nil || bucketDuration <= 0 {
			return nil, errors.New("ERR TSDB: bucketDuration must be greater than 0")
		}
		var alignment int64
		if len(args) == 7 {
			alignment, err = strconv.ParseInt(string(args[6]), 10, 64)
			if err != nil {
				return nil, errors.New("ERR TSDB: invalid alignTimestamp")
			}
		}
		if err := s.store.TimeSeriesCreateRule(
			string(args[1]),
			string(args[2]),
			string(args[4]),
			bucketDuration,
			alignment,
		); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "TS.DELETERULE":
		if err := s.store.TimeSeriesDeleteRule(string(args[1]), string(args[2])); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "TS.INFO":
		v,err:=s.store.TimeSeriesInfo(key)
		if err!=nil{
			if strings.HasPrefix(err.Error(),"WRONGTYPE "){return nil,errors.New("ERR "+err.Error())}
			return nil,err
		}
		labelItems:=make([][]byte,len(v.Labels))
		for i,label:=range v.Labels{
			labelItems[i]=array(formatBulkString([]byte(label.Key)),formatBulkString([]byte(label.Value)))
		}
		ruleItems:=make([][]byte,len(v.Rules))
		for i,rule:=range v.Rules{
			ruleItems[i]=array(
				formatBulkString([]byte(rule.DestKey)),
				integer(rule.BucketDuration),
				[]byte("+"+rule.Aggregator+"\r\n"),
				integer(rule.Alignment),
			)
		}
		sourceReply:=nullBulk()
		if v.SourceKey!=""{
			sourceReply=formatBulkString([]byte(v.SourceKey))
		}
		return array(
			[]byte("+totalSamples\r\n"),integer(int64(v.TotalSamples)),
			[]byte("+memoryUsage\r\n"),integer(v.MemoryUsage),
			[]byte("+firstTimestamp\r\n"),integer(v.FirstTimestamp),
			[]byte("+lastTimestamp\r\n"),integer(v.LastTimestamp),
			[]byte("+retentionTime\r\n"),integer(v.RetentionTime),
			[]byte("+chunkCount\r\n"),integer(int64(v.ChunkCount)),
			[]byte("+chunkSize\r\n"),integer(int64(v.ChunkSize)),
			[]byte("+chunkType\r\n"),[]byte("+compressed\r\n"),
			[]byte("+duplicatePolicy\r\n"),[]byte("+"+v.DuplicatePolicy+"\r\n"),
			[]byte("+labels\r\n"),array(labelItems...),
			[]byte("+sourceKey\r\n"),sourceReply,
			[]byte("+rules\r\n"),array(ruleItems...),
			[]byte("+ignoreMaxTimeDiff\r\n"),integer(v.IgnoreMaxTimeDiff),
			[]byte("+ignoreMaxValDiff\r\n"),formatBulkString([]byte("0")),
		),nil
	}
	return nil,errors.New("ERR unknown TimeSeries command")
}
