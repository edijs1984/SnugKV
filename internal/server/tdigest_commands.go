package server

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var tDigestCommands = map[string]commandInfo{
	"TDIGEST.CREATE":       {2, 4, 1, 1, 1, true},
	"TDIGEST.ADD":          {3, 0, 1, 1, 1, true},
	"TDIGEST.RESET":        {2, 2, 1, 1, 1, true},
	"TDIGEST.MERGE":        {4, 0, 1, 1, 1, true},
	"TDIGEST.MIN":          {2, 2, 1, 1, 1, false},
	"TDIGEST.MAX":          {2, 2, 1, 1, 1, false},
	"TDIGEST.QUANTILE":     {3, 0, 1, 1, 1, false},
	"TDIGEST.CDF":          {3, 0, 1, 1, 1, false},
	"TDIGEST.RANK":         {3, 0, 1, 1, 1, false},
	"TDIGEST.REVRANK":      {3, 0, 1, 1, 1, false},
	"TDIGEST.BYRANK":       {3, 0, 1, 1, 1, false},
	"TDIGEST.BYREVRANK":    {3, 0, 1, 1, 1, false},
	"TDIGEST.TRIMMED_MEAN": {4, 4, 1, 1, 1, false},
	"TDIGEST.INFO":         {2, 2, 1, 1, 1, false},
}

func init() {
	for name, info := range tDigestCommands {
		commandTable[name] = info
	}
}

func isTDigestCommand(args [][]byte) bool {
	if len(args) == 0 { return false }
	_, ok := tDigestCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func tDigestFloat(v float64) []byte {
	switch {
	case math.IsNaN(v):
		return []byte("nan")
	case math.IsInf(v, 1):
		return []byte("inf")
	case math.IsInf(v, -1):
		return []byte("-inf")
	default:
		return formatZSetScore(v)
	}
}

func tDigestFloatBulk(v float64) []byte { return formatBulkString(tDigestFloat(v)) }

func tDigestFloatArray(values []float64) []byte {
	items:=make([][]byte,len(values))
	for i,v:=range values { items[i]=tDigestFloatBulk(v) }
	return array(items...)
}

func tDigestIntArray(values []int64) []byte {
	items:=make([][]byte,len(values))
	for i,v:=range values { items[i]=integer(v) }
	return array(items...)
}

func parseTDigestFinite(raw []byte, parseErr, finiteErr string) (float64,error) {
	v,err:=strconv.ParseFloat(string(raw),64)
	if err!=nil || math.IsNaN(v) { return 0,errors.New(parseErr) }
	if math.IsInf(v,0) { return 0,errors.New(finiteErr) }
	return v,nil
}

func parseTDigestFloat(raw []byte, parseErr string, rejectNaN bool)(float64,error){
	v,err:=strconv.ParseFloat(string(raw),64)
	if err!=nil || rejectNaN && math.IsNaN(v){return 0,errors.New(parseErr)}
	return v,nil
}

func (s *Server) executeTDigest(args [][]byte)([]byte,error){
	if len(args)==0{return nil,errors.New("ERR empty command")}
	cmd:=strings.ToUpper(string(args[0]))
	info,ok:=tDigestCommands[cmd]
	if !ok{return nil,errors.New("ERR unknown T-Digest command")}
	if len(args)<info.min || info.max>0 && len(args)>info.max {
		return nil,errors.New("ERR wrong number of arguments for '"+strings.ToLower(cmd)+"' command")
	}
	key:=string(args[1])

	switch cmd {
	case "TDIGEST.CREATE":
		compression:=int64(100)
		if len(args)==4 {
			if !strings.EqualFold(string(args[2]),"COMPRESSION"){
				return nil,errors.New("ERR T-Digest: wrong keyword")
			}
			v,err:=strconv.ParseInt(string(args[3]),10,64)
			if err!=nil{return nil,errors.New("ERR T-Digest: error parsing compression parameter")}
			if v<=0{return nil,errors.New("ERR T-Digest: compression parameter needs to be a positive integer")}
			compression=v
		}
		if err:=s.store.TDigestCreate(key,compression);err!=nil{return nil,err}
		return []byte("+OK\r\n"),nil

	case "TDIGEST.ADD":
		// RedisBloom checks key existence/type before parsing values.
		if _,err:=s.store.TDigestInfo(key);err!=nil{return nil,err}
		values:=make([]float64,len(args)-2)
		for i:=range values {
			v,err:=strconv.ParseFloat(string(args[i+2]),64)
			if err!=nil || math.IsNaN(v){return nil,errors.New("ERR T-Digest: error parsing val parameter")}
			if math.IsInf(v,0){return nil,errors.New("ERR T-Digest: val parameter needs to be a finite number")}
			values[i]=v
		}
		if err:=s.store.TDigestAdd(key,values);err!=nil{return nil,err}
		return []byte("+OK\r\n"),nil

	case "TDIGEST.RESET":
		if err:=s.store.TDigestReset(key);err!=nil{return nil,err}
		return []byte("+OK\r\n"),nil

	case "TDIGEST.MIN","TDIGEST.MAX":
		v,err:=s.store.TDigestMinMax(key,cmd=="TDIGEST.MAX")
		if err!=nil{return nil,err}
		return tDigestFloatBulk(v),nil

	case "TDIGEST.QUANTILE":
		qs:=make([]float64,len(args)-2)
		for i:=range qs {
			v,err:=strconv.ParseFloat(string(args[i+2]),64)
			if err!=nil || math.IsNaN(v){return nil,errors.New("ERR T-Digest: error parsing quantile")}
			if v<0 || v>1{return nil,errors.New("ERR T-Digest: quantile should be in [0,1]")}
			qs[i]=v
		}
		values,err:=s.store.TDigestQuantiles(key,qs);if err!=nil{return nil,err}
		return tDigestFloatArray(values),nil

	case "TDIGEST.CDF":
		values:=make([]float64,len(args)-2)
		for i:=range values {
			v,err:=strconv.ParseFloat(string(args[i+2]),64)
			if err!=nil || math.IsNaN(v){return nil,errors.New("ERR T-Digest: error parsing cdf")}
			values[i]=v
		}
		out,err:=s.store.TDigestCDF(key,values);if err!=nil{return nil,err}
		return tDigestFloatArray(out),nil

	case "TDIGEST.RANK","TDIGEST.REVRANK":
		values:=make([]float64,len(args)-2)
		for i:=range values {
			v,err:=strconv.ParseFloat(string(args[i+2]),64)
			if err!=nil || math.IsNaN(v){return nil,errors.New("ERR T-Digest: error parsing value")}
			values[i]=v
		}
		out,err:=s.store.TDigestRanks(key,values,cmd=="TDIGEST.REVRANK");if err!=nil{return nil,err}
		return tDigestIntArray(out),nil

	case "TDIGEST.BYRANK","TDIGEST.BYREVRANK":
		ranks:=make([]int64,len(args)-2)
		for i:=range ranks {
			v,err:=strconv.ParseInt(string(args[i+2]),10,64)
			if err!=nil{return nil,errors.New("ERR T-Digest: error parsing rank")}
			if v<0{return nil,errors.New("ERR T-Digest: rank needs to be non negative")}
			ranks[i]=v
		}
		out,err:=s.store.TDigestByRanks(key,ranks,cmd=="TDIGEST.BYREVRANK");if err!=nil{return nil,err}
		return tDigestFloatArray(out),nil

	case "TDIGEST.TRIMMED_MEAN":
		low,err:=strconv.ParseFloat(string(args[2]),64)
		if err!=nil || math.IsNaN(low){return nil,errors.New("ERR T-Digest: error parsing low_cut_percentile")}
		high,err:=strconv.ParseFloat(string(args[3]),64)
		if err!=nil || math.IsNaN(high){return nil,errors.New("ERR T-Digest: error parsing high_cut_percentile")}
		if low<0 || low>1 || high<0 || high>1 {
			return nil,errors.New("ERR T-Digest: low_cut_percentile and high_cut_percentile should be in [0,1]")
		}
		if low>=high{return nil,errors.New("ERR T-Digest: low_cut_percentile should be lower than high_cut_percentile")}
		v,err:=s.store.TDigestTrimmedMean(key,low,high);if err!=nil{return nil,err}
		return tDigestFloatBulk(v),nil

	case "TDIGEST.INFO":
		v,err:=s.store.TDigestInfo(key);if err!=nil{return nil,err}
		return array(
			[]byte("+Compression\r\n"),integer(v.Compression),
			[]byte("+Capacity\r\n"),integer(int64(v.Capacity)),
			[]byte("+Merged nodes\r\n"),integer(int64(v.MergedNodes)),
			[]byte("+Unmerged nodes\r\n"),integer(int64(v.UnmergedNodes)),
			[]byte("+Merged weight\r\n"),integer(v.MergedWeight),
			[]byte("+Unmerged weight\r\n"),integer(v.UnmergedWeight),
			[]byte("+Observations\r\n"),integer(v.Observations),
			[]byte("+Total compressions\r\n"),integer(v.TotalCompressions),
			[]byte("+Memory usage\r\n"),integer(v.MemoryUsage),
		),nil

	case "TDIGEST.MERGE":
		n,err:=strconv.ParseInt(string(args[2]),10,64)
		if err!=nil{return nil,errors.New("ERR T-Digest: error parsing numkeys")}
		if n<=0{return nil,errors.New("ERR T-Digest: numkeys needs to be a positive integer")}
		if n>int64(len(args)-3){return nil,errors.New("ERR wrong number of arguments for 'tdigest.merge' command")}
		sources:=make([]string,int(n))
		for i:=range sources{sources[i]=string(args[3+i])}
		pos:=3+int(n)
		var compression *int64
		override:=false
		for pos<len(args){
			switch strings.ToUpper(string(args[pos])){
			case "COMPRESSION":
				if pos+1>=len(args){return nil,errors.New("ERR wrong number of arguments for 'tdigest.merge' command")}
				v,e:=strconv.ParseInt(string(args[pos+1]),10,64)
				if e!=nil{return nil,errors.New("ERR T-Digest: error parsing compression parameter")}
				if v<=0{return nil,errors.New("ERR T-Digest: compression parameter needs to be a positive integer")}
				compression=&v; pos+=2
			case "OVERRIDE":
				override=true;pos++
			default:
				return nil,errors.New("ERR T-Digest: wrong keyword")
			}
		}
		if err:=s.store.TDigestMerge(key,sources,compression,override);err!=nil{return nil,err}
		return []byte("+OK\r\n"),nil
	}
	return nil,errors.New("ERR unknown T-Digest command")
}
