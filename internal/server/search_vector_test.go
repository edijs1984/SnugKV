package server

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func vectorBlob(values ...float32) []byte {
	out := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out
}

func setupVectorSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)

	for _, doc := range []struct{
		key string
		json string
	}{
		{"vec:1", `{"name":"a","v":[1,0,0]}`},
		{"vec:2", `{"name":"b","v":[0.9,0.1,0]}`},
		{"vec:3", `{"name":"c","v":[0,1,0]}`},
		{"vec:4", `{"name":"d","v":[0,0,1]}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("v"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("vec:"),
		[]byte("SCHEMA"),
		[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"),
		[]byte("$.v"), []byte("AS"), []byte("v"), []byte("VECTOR"),
		[]byte("FLAT"), []byte("6"),
		[]byte("TYPE"), []byte("FLOAT32"),
		[]byte("DIM"), []byte("3"),
		[]byte("DISTANCE_METRIC"), []byte("COSINE"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFTVectorCreateInfo(t *testing.T) {
	s := setupVectorSearch(t)
	reply, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("v")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	for _, want := range []string{"VECTOR", "FLAT", "FLOAT32", "COSINE", "dim"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.INFO missing %q: %q", want, reply)
		}
	}
}

func TestFTVectorKNN(t *testing.T) {
	s := setupVectorSearch(t)
	q := vectorBlob(1, 0, 0)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("(*)=>[KNN 2 @v $q AS score]"),
		[]byte("PARAMS"), []byte("2"), []byte("q"), q,
		[]byte("SORTBY"), []byte("score"), []byte("ASC"),
		[]byte("RETURN"), []byte("2"), []byte("name"), []byte("score"),
		[]byte("DIALECT"), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	if strings.Index(got, "vec:1") < 0 || strings.Index(got, "vec:2") < 0 {
		t.Fatalf("missing KNN rows: %q", reply)
	}
	if strings.Index(got, "vec:1") > strings.Index(got, "vec:2") {
		t.Fatalf("KNN score order mismatch: %q", reply)
	}
	for _, want := range []string{"score", "name", "a", "b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("KNN reply missing %q: %q", want, reply)
		}
	}
}

func TestFTVectorRange(t *testing.T) {
	s := setupVectorSearch(t)
	q := vectorBlob(1, 0, 0)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("@v:[VECTOR_RANGE 0.02 $q]"),
		[]byte("PARAMS"), []byte("2"), []byte("q"), q,
		[]byte("RETURN"), []byte("1"), []byte("name"),
		[]byte("DIALECT"), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	for _, want := range []string{"vec:1", "vec:2", "a", "b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("range reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(got, "vec:3") || strings.Contains(got, "vec:4") {
		t.Fatalf("range leaked distant vectors: %q", reply)
	}
}

func TestFTVectorErrors(t *testing.T) {
	s := setupVectorSearch(t)

	_, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("(*)=>[KNN 2 @v $q]"),
		[]byte("DIALECT"), []byte("2"),
	})
	if err == nil || err.Error() != "SEARCH_PARAM_NOT_FOUND Parameter not found `q`" {
		t.Fatalf("missing param err=%v", err)
	}

	_, err = s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("(*)=>[KNN 2 @missing $q]"),
		[]byte("PARAMS"), []byte("2"), []byte("q"), vectorBlob(1,0,0),
		[]byte("DIALECT"), []byte("2"),
	})
	if err == nil || err.Error() != "SEARCH_SYNTAX Unknown field at offset 12 near missing" {
		t.Fatalf("unknown field err=%v", err)
	}

	_, err = s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("(*)=>[KNN 2 @v $q]"),
		[]byte("PARAMS"), []byte("2"), []byte("q"), vectorBlob(1,0),
		[]byte("DIALECT"), []byte("2"),
	})
	if err == nil || err.Error() != "SEARCH_QUERY_BAD Error parsing vector similarity query: query vector blob size (8) does not match index's expected size (12)." {
		t.Fatalf("size err=%v", err)
	}
}

func TestFTVectorKNNZero(t *testing.T) {
	s := setupVectorSearch(t)
	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("v"),
		[]byte("(*)=>[KNN 0 @v $q]"),
		[]byte("PARAMS"), []byte("2"), []byte("q"), vectorBlob(1,0,0),
		[]byte("DIALECT"), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "*1\r\n:0\r\n" {
		t.Fatalf("KNN 0 reply=%q", reply)
	}
}
