package engine

import (
	"math"
	"testing"
)

func TestTimeSeriesOracleCore(t *testing.T) {
	store := New()
	if err := store.TimeSeriesCreate("ts", 0, TimeSeriesBlock, nil); err != nil { t.Fatal(err) }
	for _, sample := range []TimeSeriesSample{{1000,1},{2000,2.5},{3000,3}} {
		if err := store.TimeSeriesAdd("ts", sample.Timestamp, sample.Value, nil); err != nil { t.Fatal(err) }
	}
	got, found, err := store.TimeSeriesGet("ts")
	if err != nil || !found || got.Timestamp != 3000 || got.Value != 3 {
		t.Fatalf("GET=%+v found=%v err=%v", got, found, err)
	}
	r, err := store.TimeSeriesRange("ts", -1<<63, 1<<63-1, false)
	if err != nil || len(r) != 3 || r[0].Timestamp != 1000 || r[1].Value != 2.5 || r[2].Timestamp != 3000 {
		t.Fatalf("RANGE=%+v err=%v", r, err)
	}
}

func TestTimeSeriesDuplicatePolicies(t *testing.T) {
	store := New()
	tests := []struct{
		key string
		policy TimeSeriesDuplicatePolicy
		first, second, want float64
	}{
		{"last",TimeSeriesLast,1,7,7},
		{"first",TimeSeriesFirst,1,7,1},
		{"sum",TimeSeriesSum,2,3,5},
		{"min",TimeSeriesMin,5,2,2},
		{"max",TimeSeriesMax,5,8,8},
	}
	for _,tc := range tests {
		if err := store.TimeSeriesCreate(tc.key,0,tc.policy,nil); err != nil { t.Fatal(err) }
		if err := store.TimeSeriesAdd(tc.key,1000,tc.first,nil); err != nil { t.Fatal(err) }
		if err := store.TimeSeriesAdd(tc.key,1000,tc.second,nil); err != nil { t.Fatal(err) }
		s,found,err:=store.TimeSeriesGet(tc.key)
		if err != nil || !found || s.Value != tc.want { t.Fatalf("%s=%+v err=%v",tc.key,s,err) }
	}
	if err:=store.TimeSeriesCreate("block",0,TimeSeriesBlock,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesAdd("block",1000,1,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesAdd("block",1000,7,nil);err==nil{
		t.Fatal("expected BLOCK duplicate error")
	}
}

func TestTimeSeriesRetentionOutOfOrderAndNaN(t *testing.T) {
	store:=New()
	labels:=[]TimeSeriesLabel{{"sensor","temp"},{"site","riga"}}
	if err:=store.TimeSeriesCreate("meta",5000,TimeSeriesLast,labels);err!=nil{t.Fatal(err)}
	for _,smp:=range []TimeSeriesSample{{10000,10},{12000,12},{16000,16},{11500,11.5}}{
		if err:=store.TimeSeriesAdd("meta",smp.Timestamp,smp.Value,nil);err!=nil{t.Fatal(err)}
	}
	r,err:=store.TimeSeriesRange("meta",-1<<63,1<<63-1,false)
	if err!=nil || len(r)!=3 || r[0].Timestamp!=11500 || r[1].Timestamp!=12000 || r[2].Timestamp!=16000{
		t.Fatalf("retained=%+v err=%v",r,err)
	}
	info,err:=store.TimeSeriesInfo("meta")
	if err!=nil{t.Fatal(err)}
	if info.TotalSamples!=3 || info.MemoryUsage!=5216 || info.FirstTimestamp!=11500 ||
		info.LastTimestamp!=16000 || info.RetentionTime!=5000 || info.DuplicatePolicy!="last"{
		t.Fatalf("info=%+v",info)
	}

	if err:=store.TimeSeriesCreate("nan",0,TimeSeriesBlock,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesAdd("nan",4000,math.NaN(),nil);err!=nil{t.Fatal(err)}
	s,found,err:=store.TimeSeriesGet("nan")
	if err!=nil || !found || !math.IsNaN(s.Value){t.Fatalf("nan=%+v err=%v",s,err)}
	if err:=store.TimeSeriesAdd("nan",500,5,nil);err!=nil{t.Fatal(err)}
	info,err=store.TimeSeriesInfo("nan")
	if err!=nil || info.FirstTimestamp!=500 || info.LastTimestamp!=4000 || info.TotalSamples!=2{
		t.Fatalf("nan info=%+v err=%v",info,err)
	}
}

func TestTimeSeriesIncrDeleteAndPersistence(t *testing.T) {
	store:=New()
	if err:=store.TimeSeriesCreate("counter",0,TimeSeriesBlock,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesIncrBy("counter",5,1000,false,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesIncrBy("counter",2,2000,false,nil);err!=nil{t.Fatal(err)}
	if err:=store.TimeSeriesIncrBy("counter",1.5,3000,true,nil);err!=nil{t.Fatal(err)}
	s,_,_:=store.TimeSeriesGet("counter")
	if s.Value!=5.5{t.Fatalf("counter=%+v",s)}
	if err:=store.TimeSeriesIncrBy("counter",1,2500,false,nil);err==nil{
		t.Fatal("expected timestamp error")
	}

	if err:=store.TimeSeriesCreate("del",0,TimeSeriesBlock,nil);err!=nil{t.Fatal(err)}
	for i:=int64(1);i<=5;i++{
		if err:=store.TimeSeriesAdd("del",i*1000,float64(i),nil);err!=nil{t.Fatal(err)}
	}
	n,err:=store.TimeSeriesDeleteRange("del",2000,4000)
	if err!=nil || n!=3{t.Fatalf("del=%d err=%v",n,err)}
	r,_:=store.TimeSeriesRange("del",-1<<63,1<<63-1,false)
	if len(r)!=2 || r[0].Timestamp!=1000 || r[1].Timestamp!=5000{t.Fatalf("remaining=%+v",r)}

	records:=store.Export([]string{"counter"})
	if len(records)!=1 || ValueType(records[0].ValueType)!=TypeTimeSeries{t.Fatalf("records=%+v",records)}
	target:=New()
	if err:=target.Restore(records,false);err!=nil{t.Fatal(err)}
	s,found,err:=target.TimeSeriesGet("counter")
	if err!=nil || !found || s.Timestamp!=3000 || s.Value!=5.5{t.Fatalf("restored=%+v found=%v err=%v",s,found,err)}
}
