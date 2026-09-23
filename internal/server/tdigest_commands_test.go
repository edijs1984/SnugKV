package server

import (
	"math"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func tdArgs(values ...string) [][]byte {
	out:=make([][]byte,len(values))
	for i,v:=range values{out[i]=[]byte(v)}
	return out
}

func TestTDigestOracleCore(t *testing.T){
	s:=New(engine.New())
	resp,err:=s.Execute(tdArgs("TDIGEST.CREATE","td"))
	if err!=nil || string(resp)!="+OK\r\n"{t.Fatalf("create=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.INFO","td"))
	wantInfo:="*18\r\n+Compression\r\n:100\r\n+Capacity\r\n:610\r\n+Merged nodes\r\n:0\r\n+Unmerged nodes\r\n:0\r\n+Merged weight\r\n:0\r\n+Unmerged weight\r\n:0\r\n+Observations\r\n:0\r\n+Total compressions\r\n:0\r\n+Memory usage\r\n:9840\r\n"
	if err!=nil || string(resp)!=wantInfo{t.Fatalf("info=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.MIN","td"))
	if err!=nil || string(resp)!="$3\r\nnan\r\n"{t.Fatalf("min=%q err=%v",resp,err)}

	if _,err=s.Execute(tdArgs("TDIGEST.ADD","td","1","2","3","4","5","10","10"));err!=nil{t.Fatal(err)}
	resp,err=s.Execute(tdArgs("TDIGEST.QUANTILE","td","0","0.25","0.5","0.75","1"))
	wantQ:="*5\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n4\r\n$2\r\n10\r\n$2\r\n10\r\n"
	if err!=nil || string(resp)!=wantQ{t.Fatalf("q=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.CDF","td","0","1","3","5","10","11"))
	wantCDF:="*6\r\n$1\r\n0\r\n$19\r\n0.07142857142857142\r\n$19\r\n0.35714285714285715\r\n$18\r\n0.6428571428571429\r\n$18\r\n0.8571428571428571\r\n$1\r\n1\r\n"
	if err!=nil || string(resp)!=wantCDF{t.Fatalf("cdf=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.RANK","td","0","1","3","5","10","11"))
	if err!=nil || string(resp)!="*6\r\n:-1\r\n:0\r\n:2\r\n:4\r\n:6\r\n:7\r\n"{t.Fatalf("rank=%q err=%v",resp,err)}
	resp,err=s.Execute(tdArgs("TDIGEST.REVRANK","td","0","1","3","5","10","11"))
	if err!=nil || string(resp)!="*6\r\n:7\r\n:6\r\n:4\r\n:2\r\n:1\r\n:-1\r\n"{t.Fatalf("revrank=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.BYRANK","td","0","1","3","6","7"))
	if err!=nil || string(resp)!="*5\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n4\r\n$2\r\n10\r\n$3\r\ninf\r\n"{t.Fatalf("byrank=%q err=%v",resp,err)}

	resp,err=s.Execute(tdArgs("TDIGEST.TRIMMED_MEAN","td","0.2","0.8"))
	if err!=nil || string(resp)!="$3\r\n4.8\r\n"{t.Fatalf("trim=%q err=%v",resp,err)}
}

func TestTDigestOracleMergeAndReset(t *testing.T){
	s:=New(engine.New())
	for _,args:=range [][]string{
		{"TDIGEST.CREATE","a","COMPRESSION","50"},
		{"TDIGEST.CREATE","b","COMPRESSION","200"},
		{"TDIGEST.ADD","a","1","2","3"},
		{"TDIGEST.ADD","b","100","200","300"},
	}{
		if _,err:=s.Execute(tdArgs(args...));err!=nil{t.Fatalf("%v err=%v",args,err)}
	}
	resp,err:=s.Execute(tdArgs("TDIGEST.MERGE","merged","2","a","b"))
	if err!=nil || string(resp)!="+OK\r\n"{t.Fatalf("merge=%q err=%v",resp,err)}
	resp,err=s.Execute(tdArgs("TDIGEST.INFO","merged"))
	if err!=nil || !strings.Contains(string(resp),"+Compression\r\n:200\r\n") ||
		!strings.Contains(string(resp),"+Merged nodes\r\n:3\r\n+Unmerged nodes\r\n:3\r\n") ||
		!strings.Contains(string(resp),"+Observations\r\n:6\r\n"){
		t.Fatalf("merged info=%q err=%v",resp,err)
	}
	if _,err=s.Execute(tdArgs("TDIGEST.QUANTILE","merged","0","0.5","1"));err!=nil{t.Fatal(err)}
	if _,err=s.Execute(tdArgs("TDIGEST.MERGE","merged","1","a"));err!=nil{t.Fatal(err)}
	resp,_=s.Execute(tdArgs("TDIGEST.INFO","merged"))
	if !strings.Contains(string(resp),"+Merged nodes\r\n:6\r\n+Unmerged nodes\r\n:3\r\n") ||
		!strings.Contains(string(resp),"+Observations\r\n:9\r\n"){
		t.Fatalf("second merge info=%q",resp)
	}
	if _,err=s.Execute(tdArgs("TDIGEST.MERGE","merged","1","b","OVERRIDE"));err!=nil{t.Fatal(err)}
	resp,_=s.Execute(tdArgs("TDIGEST.INFO","merged"))
	if !strings.Contains(string(resp),"+Merged nodes\r\n:0\r\n+Unmerged nodes\r\n:3\r\n") ||
		!strings.Contains(string(resp),"+Observations\r\n:3\r\n"){
		t.Fatalf("override info=%q",resp)
	}

	if _,err=s.Execute(tdArgs("TDIGEST.CREATE","reset"));err!=nil{t.Fatal(err)}
	if _,err=s.Execute(tdArgs("TDIGEST.ADD","reset","1","2"));err!=nil{t.Fatal(err)}
	if _,err=s.Execute(tdArgs("TDIGEST.RESET","reset"));err!=nil{t.Fatal(err)}
	resp,err=s.Execute(tdArgs("TDIGEST.MIN","reset"))
	if err!=nil || string(resp)!="$3\r\nnan\r\n"{t.Fatalf("reset min=%q err=%v",resp,err)}
}

func TestTDigestOracleErrors(t *testing.T){
	s:=New(engine.New())
	if _,err:=s.Execute(tdArgs("TDIGEST.CREATE","td"));err!=nil{t.Fatal(err)}
	for _,tc:=range []struct{args []string; want string}{
		{[]string{"TDIGEST.CREATE","bad0","COMPRESSION","0"},"ERR T-Digest: compression parameter needs to be a positive integer"},
		{[]string{"TDIGEST.CREATE","badword","BOGUS","100"},"ERR T-Digest: wrong keyword"},
		{[]string{"TDIGEST.ADD","td","nan"},"ERR T-Digest: error parsing val parameter"},
		{[]string{"TDIGEST.ADD","td","inf"},"ERR T-Digest: val parameter needs to be a finite number"},
		{[]string{"TDIGEST.QUANTILE","td","-0.1"},"ERR T-Digest: quantile should be in [0,1]"},
		{[]string{"TDIGEST.CDF","td","nan"},"ERR T-Digest: error parsing cdf"},
		{[]string{"TDIGEST.RANK","td","nan"},"ERR T-Digest: error parsing value"},
		{[]string{"TDIGEST.BYRANK","td","-1"},"ERR T-Digest: rank needs to be non negative"},
		{[]string{"TDIGEST.TRIMMED_MEAN","td","0.8","0.2"},"ERR T-Digest: low_cut_percentile should be lower than high_cut_percentile"},
		{[]string{"TDIGEST.INFO","missing"},"ERR T-Digest: key does not exist"},
	}{
		if _,err:=s.Execute(tdArgs(tc.args...));err==nil || err.Error()!=tc.want{
			t.Fatalf("%v err=%v want=%q",tc.args,err,tc.want)
		}
	}
	if _,err:=s.Execute(tdArgs("SET","plain","value"));err!=nil{t.Fatal(err)}
	for _,args:=range [][]string{{"TDIGEST.INFO","plain"},{"TDIGEST.ADD","plain","1"}}{
		if _,err:=s.Execute(tdArgs(args...));err==nil || !strings.HasPrefix(err.Error(),"WRONGTYPE "){
			t.Fatalf("%v err=%v",args,err)
		}
	}
	if math.IsNaN(0) { t.Fatal("unreachable") }
}
