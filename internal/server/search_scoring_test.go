package server

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func searchScores(t *testing.T, s *Server, index, query string, extra ...string) ([]string, map[string]float64) {
	t.Helper()
	args := [][]byte{
		[]byte("FT.SEARCH"), []byte(index), []byte(query),
		[]byte("WITHSCORES"), []byte("NOCONTENT"),
	}
	for _, arg := range extra {
		args = append(args, []byte(arg))
	}
	reply, err := s.Execute(args)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(reply), "\r\n")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "*") || !strings.HasPrefix(parts[1], ":") {
		t.Fatalf("bad reply %q", reply)
	}

	keys := make([]string, 0)
	scores := make(map[string]float64)
	for i := 2; i+3 < len(parts); {
		if !strings.HasPrefix(parts[i], "$") {
			break
		}
		key := parts[i+1]
		if !strings.HasPrefix(parts[i+2], "$") {
			t.Fatalf("missing score bulk string in %q", reply)
		}
		score, err := strconv.ParseFloat(parts[i+3], 64)
		if err != nil {
			t.Fatalf("score %q: %v", parts[i+3], err)
		}
		keys = append(keys, key)
		scores[key] = score
		i += 4
	}
	return keys, scores
}

func requireScoreClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 2e-8 {
		t.Fatalf("score=%0.17g want=%0.17g", got, want)
	}
}

func TestFTSearchWithScoresBM25Controlled(t *testing.T) {
	s := newSearchTestServer(t)

	docs := []struct{ key, value string }{
		{"iso:1", `{"title":"memory"}`},
		{"iso:2", `{"title":"memory memory"}`},
		{"iso:3", `{"title":"memory alpha beta gamma"}`},
		{"iso:4", `{"title":"alpha memory"}`},
		{"iso:5", `{"title":"alpha beta gamma delta"}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("iso1"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("iso:"),
		[]byte("SCHEMA"), []byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, scores := searchScores(t, s, "iso1", "@title:memory")
	requireSearchKeys(t, keys, "iso:2", "iso:1", "iso:4", "iso:3")
	requireScoreClose(t, scores["iso:2"], 0.8460367446819628)
	requireScoreClose(t, scores["iso:1"], 0.7689446095439446)
	requireScoreClose(t, scores["iso:4"], 0.6353441920936669)
	requireScoreClose(t, scores["iso:3"], 0.4715018478678896)
}

func TestFTSearchWithScoresWildcardAll(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct{ key, value string }{
		{"iso:1", `{"title":"memory"}`},
		{"iso:2", `{"title":"memory memory"}`},
		{"iso:3", `{"title":"memory alpha beta gamma"}`},
		{"iso:4", `{"title":"alpha memory"}`},
		{"iso:5", `{"title":"alpha beta gamma delta"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("iso1"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("iso:"),
		[]byte("SCHEMA"), []byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, scores := searchScores(t, s, "iso1", "*")
	requireSearchKeys(t, keys, "iso:1", "iso:2", "iso:4", "iso:3", "iso:5")
	requireScoreClose(t, scores["iso:1"], 1.3364486062523577)
	requireScoreClose(t, scores["iso:2"], 1.104247106326305)
	requireScoreClose(t, scores["iso:4"], 1.104247106326305)
	requireScoreClose(t, scores["iso:3"], 0.8194842380157688)
	requireScoreClose(t, scores["iso:5"], 0.8194842380157688)
}

func TestFTSearchWithScoresFieldWeights(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct{ key, value string }{
		{"iso:1", `{"title":"memory","body":"alpha"}`},
		{"iso:2", `{"title":"alpha","body":"memory"}`},
		{"iso:3", `{"title":"memory","body":"memory"}`},
		{"iso:4", `{"title":"memory memory","body":"alpha"}`},
		{"iso:5", `{"title":"alpha","body":"alpha"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("isow"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("iso:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"), []byte("WEIGHT"), []byte("5"),
		[]byte("$.body"), []byte("AS"), []byte("body"), []byte("TEXT"), []byte("WEIGHT"), []byte("1"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, scores := searchScores(t, s, "isow", "memory")
	requireSearchKeys(t, keys, "iso:3", "iso:4", "iso:1", "iso:2")
	requireScoreClose(t, scores["iso:3"], 0.9491278413584009)
	requireScoreClose(t, scores["iso:4"], 0.8810735839455638)
	requireScoreClose(t, scores["iso:1"], 0.8267504344545498)
	requireScoreClose(t, scores["iso:2"], 0.6110764028585098)
}

func TestFTSearchWithScoresLimitAndSortBy(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct{ key, value string }{
		{"doc:1", `{"title":"memory","rank":30}`},
		{"doc:2", `{"title":"memory memory","rank":10}`},
		{"doc:3", `{"title":"memory alpha beta","rank":20}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("idx"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("$.rank"), []byte("AS"), []byte("rank"), []byte("NUMERIC"), []byte("SORTABLE"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, _ := searchScores(t, s, "idx", "memory", "LIMIT", "0", "2")
	if len(keys) != 2 {
		t.Fatalf("keys=%v", keys)
	}

	keys, _ = searchScores(t, s, "idx", "memory", "SORTBY", "rank", "ASC")
	requireSearchKeys(t, keys, "doc:2", "doc:3", "doc:1")
}

func TestFTSearchWithScoresDuplicateOption(t *testing.T) {
	s := newSearchTestServer(t)
	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"), []byte("doc:1"), []byte("$"), []byte(`{"title":"memory"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("idx"), []byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"), []byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("idx"), []byte("memory"),
		[]byte("WITHSCORES"), []byte("WITHSCORES"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "doc:1") {
		t.Fatalf("reply=%q", reply)
	}
}


func TestFTSearchWithScoresFieldMaskUsesSharedPostingStats(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct{ key, value string }{
		{"iso:1", `{"title":"memory","body":"alpha"}`},
		{"iso:2", `{"title":"alpha","body":"memory"}`},
		{"iso:3", `{"title":"memory","body":"memory"}`},
		{"iso:4", `{"title":"memory memory","body":"alpha"}`},
		{"iso:5", `{"title":"alpha","body":"alpha"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("iso2"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("iso:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("$.body"), []byte("AS"), []byte("body"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	_, all := searchScores(t, s, "iso2", "memory")
	titleKeys, title := searchScores(t, s, "iso2", "@title:memory")
	bodyKeys, body := searchScores(t, s, "iso2", "@body:memory")

	requireSearchKeys(t, titleKeys, "iso:3", "iso:4", "iso:1")
	requireSearchKeys(t, bodyKeys, "iso:3", "iso:2")

	for _, key := range titleKeys {
		requireScoreClose(t, title[key], all[key])
	}
	for _, key := range bodyKeys {
		requireScoreClose(t, body[key], all[key])
	}
}
