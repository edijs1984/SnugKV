package engine

import (
	"math"
	"testing"
)

func TestTDigestOracleCore(t *testing.T) {
	store := New()
	if err := store.TDigestCreate("td", 100); err != nil { t.Fatal(err) }

	info, err := store.TDigestInfo("td")
	if err != nil { t.Fatal(err) }
	if info.Compression != 100 || info.Capacity != 610 || info.MemoryUsage != 9840 ||
		info.MergedNodes != 0 || info.UnmergedNodes != 0 || info.Observations != 0 {
		t.Fatalf("empty info=%+v", info)
	}

	min, err := store.TDigestMinMax("td", false)
	if err != nil || !math.IsNaN(min) { t.Fatalf("min=%v err=%v", min, err) }
	max, err := store.TDigestMinMax("td", true)
	if err != nil || !math.IsNaN(max) { t.Fatalf("max=%v err=%v", max, err) }

	if err := store.TDigestAdd("td", []float64{1,2,3,4,5,10,10}); err != nil { t.Fatal(err) }

	q, err := store.TDigestQuantiles("td", []float64{0,0.25,0.5,0.75,1})
	if err != nil { t.Fatal(err) }
	wantQ := []float64{1,2,4,10,10}
	for i := range wantQ {
		if q[i] != wantQ[i] { t.Fatalf("quantile[%d]=%v want=%v all=%v", i,q[i],wantQ[i],q) }
	}

	cdf, err := store.TDigestCDF("td", []float64{0,1,3,5,10,11})
	if err != nil { t.Fatal(err) }
	wantCDF := []float64{0,1.0/14.0,5.0/14.0,9.0/14.0,12.0/14.0,1}
	for i := range wantCDF {
		if math.Abs(cdf[i]-wantCDF[i]) > 1e-15 { t.Fatalf("cdf[%d]=%.17g want=%.17g", i,cdf[i],wantCDF[i]) }
	}

	ranks, err := store.TDigestRanks("td", []float64{0,1,3,5,10,11}, false)
	if err != nil { t.Fatal(err) }
	wantRanks := []int64{-1,0,2,4,6,7}
	for i := range wantRanks { if ranks[i]!=wantRanks[i] { t.Fatalf("ranks=%v",ranks) } }

	rev, err := store.TDigestRanks("td", []float64{0,1,3,5,10,11}, true)
	if err != nil { t.Fatal(err) }
	wantRev := []int64{7,6,4,2,1,-1}
	for i := range wantRev { if rev[i]!=wantRev[i] { t.Fatalf("revranks=%v",rev) } }

	by, err := store.TDigestByRanks("td", []int64{0,1,3,6,7}, false)
	if err != nil { t.Fatal(err) }
	wantBy := []float64{1,2,4,10,math.Inf(1)}
	for i := range wantBy {
		if math.IsInf(wantBy[i],1) {
			if !math.IsInf(by[i],1) { t.Fatalf("byrank=%v",by) }
		} else if by[i]!=wantBy[i] { t.Fatalf("byrank=%v",by) }
	}

	mean, err := store.TDigestTrimmedMean("td",0.2,0.8)
	if err != nil { t.Fatal(err) }
	if math.Abs(mean-4.8) > 1e-12 { t.Fatalf("trimmed mean=%v",mean) }

	info, err = store.TDigestInfo("td")
	if err != nil { t.Fatal(err) }
	if info.MergedNodes != 7 || info.UnmergedNodes != 0 || info.MergedWeight != 7 ||
		info.TotalCompressions != 1 {
		t.Fatalf("post-read info=%+v",info)
	}
}

func TestTDigestMergeOracle(t *testing.T) {
	store := New()
	if err := store.TDigestCreate("a",50); err != nil { t.Fatal(err) }
	if err := store.TDigestCreate("b",200); err != nil { t.Fatal(err) }
	if err := store.TDigestAdd("a",[]float64{1,2,3}); err != nil { t.Fatal(err) }
	if err := store.TDigestAdd("b",[]float64{100,200,300}); err != nil { t.Fatal(err) }

	if err := store.TDigestMerge("merged",[]string{"a","b"},nil,false); err != nil { t.Fatal(err) }
	info, err := store.TDigestInfo("merged")
	if err != nil { t.Fatal(err) }
	if info.Compression!=200 || info.Capacity!=1210 || info.MergedNodes!=3 ||
		info.UnmergedNodes!=3 || info.MergedWeight!=3 || info.UnmergedWeight!=3 ||
		info.Observations!=6 || info.TotalCompressions!=1 || info.MemoryUsage!=19440 {
		t.Fatalf("merged info=%+v",info)
	}
	q,err:=store.TDigestQuantiles("merged",[]float64{0,0.5,1})
	if err!=nil{t.Fatal(err)}
	if q[0]!=1 || q[1]!=100 || q[2]!=300 { t.Fatalf("merged q=%v",q) }

	if err:=store.TDigestMerge("merged",[]string{"a"},nil,false);err!=nil{t.Fatal(err)}
	info,_=store.TDigestInfo("merged")
	if info.MergedNodes!=6 || info.UnmergedNodes!=3 || info.MergedWeight!=6 ||
		info.UnmergedWeight!=3 || info.Observations!=9 || info.TotalCompressions!=1 {
		t.Fatalf("second merge info=%+v",info)
	}

	if err:=store.TDigestMerge("merged",[]string{"b"},nil,true);err!=nil{t.Fatal(err)}
	info,_=store.TDigestInfo("merged")
	if info.Compression!=200 || info.MergedNodes!=0 || info.UnmergedNodes!=3 ||
		info.MergedWeight!=0 || info.UnmergedWeight!=3 || info.TotalCompressions!=0 {
		t.Fatalf("override info=%+v",info)
	}
}

func TestTDigestTTLAndPersistence(t *testing.T) {
	store:=New()
	if err:=store.TDigestCreate("td",100);err!=nil{t.Fatal(err)}
	if !store.Expire("td",60_000_000){t.Fatal("expire failed")}
	before:=store.TTL("td",true)
	if err:=store.TDigestAdd("td",[]float64{1,2,3});err!=nil{t.Fatal(err)}
	after:=store.TTL("td",true)
	if after<=0 || after>before {t.Fatalf("ttl before=%d after=%d",before,after)}
	records:=store.Export(nil)
	if len(records)!=1 || ValueType(records[0].ValueType)!=TypeTDigest {t.Fatalf("records=%+v",records)}
	target:=New()
	if err:=target.Restore(records,false);err!=nil{t.Fatal(err)}
	q,err:=target.TDigestQuantiles("td",[]float64{0.5})
	if err!=nil || len(q)!=1 || q[0]!=2 {t.Fatalf("restored q=%v err=%v",q,err)}
}
