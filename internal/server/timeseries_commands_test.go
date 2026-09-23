package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func tsArgs(values ...string) [][]byte {
	out:=make([][]byte,len(values))
	for i,v:=range values{out[i]=[]byte(v)}
	return out
}

func TestTimeSeriesOracleCore(t *testing.T){
	s:=New(engine.New())
	for _,args:=range [][]string{
		{"TS.CREATE","ts"},
		{"TS.ADD","ts","1000","1"},
		{"TS.ADD","ts","2000","2.5"},
		{"TS.ADD","ts","3000","3"},
	}{
		if _,err:=s.Execute(tsArgs(args...));err!=nil{t.Fatalf("%v err=%v",args,err)}
	}
	resp,err:=s.Execute(tsArgs("TS.GET","ts"))
	if err!=nil || string(resp)!="*2\r\n:3000\r\n+3\r\n"{t.Fatalf("get=%q err=%v",resp,err)}
	resp,err=s.Execute(tsArgs("TS.RANGE","ts","-","+"))
	want:="*3\r\n*2\r\n:1000\r\n+1\r\n*2\r\n:2000\r\n+2.5\r\n*2\r\n:3000\r\n+3\r\n"
	if err!=nil || string(resp)!=want{t.Fatalf("range=%q err=%v",resp,err)}
	resp,err=s.Execute(tsArgs("TS.REVRANGE","ts","-","+"))
	want="*3\r\n*2\r\n:3000\r\n+3\r\n*2\r\n:2000\r\n+2.5\r\n*2\r\n:1000\r\n+1\r\n"
	if err!=nil || string(resp)!=want{t.Fatalf("revrange=%q err=%v",resp,err)}
}

func TestTimeSeriesOraclePoliciesAndInfo(t *testing.T){
	s:=New(engine.New())
	for _,args:=range [][]string{
		{"TS.CREATE","last","DUPLICATE_POLICY","LAST"},
		{"TS.ADD","last","1000","1"},
		{"TS.ADD","last","1000","7"},
		{"TS.CREATE","sum","DUPLICATE_POLICY","SUM"},
		{"TS.ADD","sum","1000","2"},
		{"TS.ADD","sum","1000","3"},
		{"TS.CREATE","minp","DUPLICATE_POLICY","MIN"},
		{"TS.ADD","minp","1000","5"},
		{"TS.ADD","minp","1000","2"},
		{"TS.CREATE","maxp","DUPLICATE_POLICY","MAX"},
		{"TS.ADD","maxp","1000","5"},
		{"TS.ADD","maxp","1000","8"},
	}{
		if _,err:=s.Execute(tsArgs(args...));err!=nil{t.Fatalf("%v err=%v",args,err)}
	}
	for _,tc:=range []struct{key,want string}{
		{"last","*2\r\n:1000\r\n+7\r\n"},
		{"sum","*2\r\n:1000\r\n+5\r\n"},
		{"minp","*2\r\n:1000\r\n+2\r\n"},
		{"maxp","*2\r\n:1000\r\n+8\r\n"},
	}{
		resp,err:=s.Execute(tsArgs("TS.GET",tc.key))
		if err!=nil || string(resp)!=tc.want{t.Fatalf("%s=%q err=%v",tc.key,resp,err)}
	}

	if _,err:=s.Execute(tsArgs("TS.CREATE","meta","RETENTION","5000","DUPLICATE_POLICY","LAST","LABELS","sensor","temp","site","riga"));err!=nil{t.Fatal(err)}
	for _,args:=range [][]string{
		{"TS.ADD","meta","10000","10"},{"TS.ADD","meta","12000","12"},
		{"TS.ADD","meta","16000","16"},{"TS.ADD","meta","11500","11.5"},
	}{
		if _,err:=s.Execute(tsArgs(args...));err!=nil{t.Fatal(err)}
	}
	resp,err:=s.Execute(tsArgs("TS.INFO","meta"))
	if err!=nil ||
		!strings.Contains(string(resp),"+totalSamples\r\n:3\r\n") ||
		!strings.Contains(string(resp),"+memoryUsage\r\n:5216\r\n") ||
		!strings.Contains(string(resp),"+firstTimestamp\r\n:11500\r\n+lastTimestamp\r\n:16000\r\n") ||
		!strings.Contains(string(resp),"+retentionTime\r\n:5000\r\n") ||
		!strings.Contains(string(resp),"+duplicatePolicy\r\n+last\r\n"){
		t.Fatalf("info=%q err=%v",resp,err)
	}
}

func TestTimeSeriesOracleIncrDeleteNaNAndErrors(t *testing.T){
	s:=New(engine.New())
	if _,err:=s.Execute(tsArgs("TS.CREATE","counter"));err!=nil{t.Fatal(err)}
	for _,args:=range [][]string{
		{"TS.INCRBY","counter","5","TIMESTAMP","1000"},
		{"TS.INCRBY","counter","2","TIMESTAMP","2000"},
		{"TS.DECRBY","counter","1.5","TIMESTAMP","3000"},
	}{
		if _,err:=s.Execute(tsArgs(args...));err!=nil{t.Fatalf("%v err=%v",args,err)}
	}
	resp,err:=s.Execute(tsArgs("TS.GET","counter"))
	if err!=nil || string(resp)!="*2\r\n:3000\r\n+5.5\r\n"{t.Fatalf("counter=%q err=%v",resp,err)}
	if _,err:=s.Execute(tsArgs("TS.INCRBY","counter","1","TIMESTAMP","2500"));err==nil ||
		err.Error()!="TSDB: timestamp must be equal to or higher than the maximum existing timestamp"{
		t.Fatalf("old timestamp err=%v",err)
	}

	if _,err:=s.Execute(tsArgs("TS.CREATE","ts"));err!=nil{t.Fatal(err)}
	if _,err:=s.Execute(tsArgs("TS.ADD","ts","4000","nan"));err!=nil{t.Fatal(err)}
	resp,err=s.Execute(tsArgs("TS.GET","ts"))
	if err!=nil || string(resp)!="*2\r\n:4000\r\n+NaN\r\n"{t.Fatalf("nan get=%q err=%v",resp,err)}
	if _,err:=s.Execute(tsArgs("TS.ADD","ts","500","5"));err!=nil{t.Fatal(err)}
	resp,err=s.Execute(tsArgs("TS.RANGE","ts","-","+"))
	if err!=nil || !strings.Contains(string(resp),":500\r\n+5\r\n") || !strings.Contains(string(resp),":4000\r\n+NaN\r\n"){
		t.Fatalf("nan range=%q err=%v",resp,err)
	}

	if _,err:=s.Execute(tsArgs("TS.CREATE","defaultdup"));err!=nil{t.Fatal(err)}
	if _,err:=s.Execute(tsArgs("TS.ADD","defaultdup","1000","1"));err!=nil{t.Fatal(err)}
	if _,err:=s.Execute(tsArgs("TS.ADD","defaultdup","1000","7"));err==nil ||
		!strings.Contains(err.Error(),"DUPLICATE_POLICY is set to BLOCK mode"){
		t.Fatalf("block duplicate err=%v",err)
	}

	for _,tc:=range []struct{args []string; prefix string}{
		{[]string{"TS.GET","missing"},"ERR TSDB: the key does not exist"},
		{[]string{"TS.CREATE","badret","RETENTION","-1"},"TSDB: Couldn't parse RETENTION"},
		{[]string{"TS.CREATE","baddup","DUPLICATE_POLICY","BOGUS"},"ERR TSDB: Unknown DUPLICATE_POLICY"},
	}{
		if _,err:=s.Execute(tsArgs(tc.args...));err==nil || err.Error()!=tc.prefix{
			t.Fatalf("%v err=%v want=%q",tc.args,err,tc.prefix)
		}
	}
	if _,err:=s.Execute(tsArgs("SET","plain","value"));err!=nil{t.Fatal(err)}
	if _,err:=s.Execute(tsArgs("TS.GET","plain"));err==nil || err.Error()!="ERR WRONGTYPE Operation against a key holding the wrong kind of value"{
		t.Fatalf("get plain err=%v",err)
	}
	if _,err:=s.Execute(tsArgs("TS.INFO","plain"));err==nil || err.Error()!="ERR WRONGTYPE Operation against a key holding the wrong kind of value"{
		t.Fatalf("info plain err=%v",err)
	}
	if _,err:=s.Execute(tsArgs("TS.ADD","plain","1000","1"));err==nil || err.Error()!="ERR TSDB: the key is not a TSDB key"{
		t.Fatalf("add plain err=%v",err)
	}
}
