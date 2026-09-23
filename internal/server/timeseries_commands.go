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
	"TS.INFO":     {2, 2, 1, 1, 1, false},
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
			[]byte("+sourceKey\r\n"),nullBulk(),
			[]byte("+rules\r\n"),array(),
			[]byte("+ignoreMaxTimeDiff\r\n"),integer(v.IgnoreMaxTimeDiff),
			[]byte("+ignoreMaxValDiff\r\n"),formatBulkString([]byte("0")),
		),nil
	}
	return nil,errors.New("ERR unknown TimeSeries command")
}
