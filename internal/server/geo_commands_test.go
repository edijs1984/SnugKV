package server

import (
	"math"
	"snugkv/internal/engine"
	"strings"
	"testing"
)

func geoArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func seedSicily(t *testing.T, s *Server) {
	t.Helper()
	response, err := s.Execute(geoArgs(
		"GEOADD", "Sicily",
		"13.361389", "38.115556", "Palermo",
		"15.087269", "37.502669", "Catania",
	))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("GEOADD Sicily = %q, err=%v", response, err)
	}
}

func TestGeoAddPositionHashAndDistance(t *testing.T) {
	s := New(engine.New())
	seedSicily(t, s)

	response, err := s.Execute(geoArgs("TYPE", "Sicily"))
	if err != nil || string(response) != "+zset\r\n" {
		t.Fatalf("TYPE Sicily = %q, err=%v", response, err)
	}

	palermoScore, found, err := s.store.ZSetScore("Sicily", []byte("Palermo"))
	if err != nil || !found || palermoScore != 3479099956230698 {
		t.Fatalf("Palermo score = %.0f, found=%v, err=%v", palermoScore, found, err)
	}
	cataniaScore, found, err := s.store.ZSetScore("Sicily", []byte("Catania"))
	if err != nil || !found || cataniaScore != 3479447370796909 {
		t.Fatalf("Catania score = %.0f, found=%v, err=%v", cataniaScore, found, err)
	}

	response, err = s.Execute(geoArgs("GEOPOS", "Sicily", "Palermo", "Catania", "missing"))
	if err != nil {
		t.Fatal(err)
	}
	position := string(response)
	for _, expected := range []string{"13.361389338970184", "38.1155563954963", "15.087267458438873", "37.50266842333162", "*-1\r\n"} {
		if !strings.Contains(position, expected) {
			t.Fatalf("GEOPOS missing %q in %q", expected, position)
		}
	}

	response, err = s.Execute(geoArgs("GEOHASH", "Sicily", "Palermo", "Catania", "missing"))
	if err != nil {
		t.Fatal(err)
	}
	hashes := string(response)
	if !strings.Contains(hashes, "sqc8b49rny0") || !strings.Contains(hashes, "sqdtr74hyu0") || !strings.Contains(hashes, "$-1\r\n") {
		t.Fatalf("GEOHASH = %q", hashes)
	}

	response, err = s.Execute(geoArgs("GEODIST", "Sicily", "Palermo", "Catania"))
	if err != nil || !strings.Contains(string(response), "166274.1516") {
		t.Fatalf("GEODIST meters = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("GEODIST", "Sicily", "Palermo", "Catania", "km"))
	if err != nil || !strings.Contains(string(response), "166.2742") {
		t.Fatalf("GEODIST km = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("GEODIST", "Sicily", "Palermo", "missing"))
	if err != nil || string(response) != "$-1\r\n" {
		t.Fatalf("GEODIST missing = %q, err=%v", response, err)
	}
}

func TestGeoAddOptionsTTLAndWrongType(t *testing.T) {
	s := New(engine.New())
	seedSicily(t, s)

	response, err := s.Execute(geoArgs("PEXPIRE", "Sicily", "60000"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("PEXPIRE = %q, err=%v", response, err)
	}
	before := s.store.TTL("Sicily", true)
	response, err = s.Execute(geoArgs("GEOADD", "Sicily", "CH", "13.361389", "38.115556", "Palermo"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("unchanged GEOADD CH = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("GEOADD", "Sicily", "CH", "13.4", "38.2", "Palermo"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("changed GEOADD CH = %q, err=%v", response, err)
	}
	after := s.store.TTL("Sicily", true)
	if after <= 0 || after > before {
		t.Fatalf("GEOADD did not preserve TTL: before=%d after=%d", before, after)
	}

	response, err = s.Execute(geoArgs("GEOADD", "Sicily", "NX", "10", "40", "Palermo"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("GEOADD NX existing = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("GEOADD", "Sicily", "XX", "10", "40", "Messina"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("GEOADD XX missing = %q, err=%v", response, err)
	}

	_, _ = s.Execute(geoArgs("SET", "plain", "hello"))
	if _, err := s.Execute(geoArgs("GEOADD", "plain", "13", "38", "member")); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("GEOADD wrong type error = %v", err)
	}
	if _, err := s.Execute(geoArgs("GEOPOS", "plain", "member")); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("GEOPOS wrong type error = %v", err)
	}
	if _, err := s.Execute(geoArgs("GEOADD", "bad", "181", "0", "member")); err == nil || !strings.Contains(err.Error(), "invalid longitude,latitude pair") {
		t.Fatalf("invalid coordinate error = %v", err)
	}
}

func TestGeoSearchRadiusBoxAndOptions(t *testing.T) {
	s := New(engine.New())
	seedSicily(t, s)

	response, err := s.Execute(geoArgs("GEOSEARCH", "Sicily", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC"))
	if err != nil {
		t.Fatal(err)
	}
	result := string(response)
	catania := strings.Index(result, "Catania")
	palermo := strings.Index(result, "Palermo")
	if catania < 0 || palermo < 0 || catania >= palermo {
		t.Fatalf("GEOSEARCH radius order = %q", result)
	}

	response, err = s.Execute(geoArgs("GEOSEARCH", "Sicily", "FROMMEMBER", "Catania", "BYBOX", "400", "400", "km", "ASC", "WITHDIST", "WITHHASH", "WITHCOORD"))
	if err != nil {
		t.Fatal(err)
	}
	result = string(response)
	for _, expected := range []string{"Catania", "Palermo", "3479447370796909", "15.087267458438873", "37.50266842333162"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("GEOSEARCH options missing %q in %q", expected, result)
		}
	}

	response, err = s.Execute(geoArgs("GEOSEARCH", "Sicily", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "COUNT", "1"))
	if err != nil || !strings.Contains(string(response), "Catania") || strings.Contains(string(response), "Palermo") {
		t.Fatalf("GEOSEARCH COUNT = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs("GEOSEARCH", "missing", "FROMMEMBER", "nobody", "BYRADIUS", "1", "km"))
	if err != nil || string(response) != "*0\r\n" {
		t.Fatalf("GEOSEARCH missing source = %q, err=%v", response, err)
	}
	if _, err := s.Execute(geoArgs("GEOSEARCH", "Sicily", "FROMMEMBER", "missing", "BYRADIUS", "1", "km")); err == nil || !strings.Contains(err.Error(), "could not decode") {
		t.Fatalf("GEOSEARCH missing member error = %v", err)
	}
}

func TestGeoSearchStoreReplacesDestinationAndClearsTTL(t *testing.T) {
	s := New(engine.New())
	seedSicily(t, s)
	_, _ = s.Execute(geoArgs("SET", "nearby", "old-value"))
	_, _ = s.Execute(geoArgs("PEXPIRE", "nearby", "60000"))

	response, err := s.Execute(geoArgs("GEOSEARCHSTORE", "nearby", "Sicily", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "STOREDIST"))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("GEOSEARCHSTORE = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("TYPE", "nearby"))
	if err != nil || string(response) != "+zset\r\n" {
		t.Fatalf("stored TYPE = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("ZCARD", "nearby"))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("stored ZCARD = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("PTTL", "nearby"))
	if err != nil || string(response) != ":-1\r\n" {
		t.Fatalf("stored TTL = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs("GEOSEARCHSTORE", "nearby", "missing", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("missing-source GEOSEARCHSTORE = %q, err=%v", response, err)
	}
	response, err = s.Execute(geoArgs("EXISTS", "nearby"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("destination not deleted = %q, err=%v", response, err)
	}
}


func seedLegacyGeoCities(t *testing.T, s *Server) {
	t.Helper()

	response, err := s.Execute(geoArgs(
		"GEOADD", "geo:src",
		"13.361389", "38.115556", "Palermo",
		"15.087269", "37.502669", "Catania",
		"12.496366", "41.902782", "Rome",
		"9.190000", "45.464200", "Milan",
	))
	if err != nil || string(response) != ":4\r\n" {
		t.Fatalf("GEOADD legacy cities = %q, err=%v", response, err)
	}
}

func TestLegacyGeoRadiusCore(t *testing.T) {
	s := New(engine.New())
	seedLegacyGeoCities(t, s)

	response, err := s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"200",
		"km",
	))
	if err != nil ||
		string(response) != "*2\r\n$7\r\nPalermo\r\n$7\r\nCatania\r\n" {
		t.Fatalf("GEORADIUS basic = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"2000",
		"km",
		"ASC",
	))
	if err != nil {
		t.Fatal(err)
	}
	got := string(response)
	for _, name := range []string{"Catania", "Palermo", "Rome", "Milan"} {
		if !strings.Contains(got, name) {
			t.Fatalf("GEORADIUS ASC missing %s in %q", name, got)
		}
	}
	if !(strings.Index(got, "Catania") <
		strings.Index(got, "Palermo") &&
		strings.Index(got, "Palermo") <
			strings.Index(got, "Rome") &&
		strings.Index(got, "Rome") <
			strings.Index(got, "Milan")) {
		t.Fatalf("GEORADIUS ASC order = %q", got)
	}

	response, err = s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"2000",
		"km",
		"WITHDIST",
		"WITHHASH",
		"WITHCOORD",
		"ASC",
		"COUNT",
		"3",
	))
	if err != nil {
		t.Fatal(err)
	}
	got = string(response)
	for _, want := range []string{
		"Catania",
		"56.4413",
		"3479447370796909",
		"15.087267458438873",
		"37.50266842333162",
		"Palermo",
		"190.4424",
		"Rome",
		"586.1050",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("GEORADIUS enriched missing %q in %q", want, got)
		}
	}
}

func TestLegacyGeoRadiusByMember(t *testing.T) {
	s := New(engine.New())
	seedLegacyGeoCities(t, s)

	response, err := s.Execute(geoArgs(
		"GEORADIUSBYMEMBER",
		"geo:src",
		"Palermo",
		"2000",
		"km",
		"WITHDIST",
		"WITHHASH",
		"WITHCOORD",
		"ASC",
	))
	if err != nil {
		t.Fatal(err)
	}
	got := string(response)
	for _, want := range []string{
		"Palermo",
		"0.0000",
		"Catania",
		"166.2742",
		"Rome",
		"427.6295",
		"Milan",
		"887.2253",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf(
				"GEORADIUSBYMEMBER missing %q in %q",
				want,
				got,
			)
		}
	}

	_, err = s.Execute(geoArgs(
		"GEORADIUSBYMEMBER",
		"geo:src",
		"Nope",
		"200",
		"km",
	))
	if err == nil ||
		err.Error() != "ERR could not decode requested zset member" {
		t.Fatalf("missing member error = %v", err)
	}
}

func TestLegacyGeoRadiusStore(t *testing.T) {
	s := New(engine.New())
	seedLegacyGeoCities(t, s)

	response, err := s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"2000",
		"km",
		"STORE",
		"geo:store",
	))
	if err != nil || string(response) != ":4\r\n" {
		t.Fatalf("GEORADIUS STORE = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs(
		"ZRANGE",
		"geo:store",
		"0",
		"-1",
		"WITHSCORES",
	))
	if err != nil {
		t.Fatal(err)
	}
	got := string(response)
	for _, want := range []string{
		"Palermo",
		"3479099956230698",
		"Catania",
		"3479447370796909",
		"Rome",
		"3480343273965391",
		"Milan",
		"3663062935203680",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stored GEO score missing %q in %q", want, got)
		}
	}

	response, err = s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"2000",
		"km",
		"STOREDIST",
		"geo:dist",
	))
	if err != nil || string(response) != ":4\r\n" {
		t.Fatalf("GEORADIUS STOREDIST = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs(
		"ZSCORE",
		"geo:dist",
		"Catania",
	))
	if err != nil || !strings.Contains(
		string(response),
		"56.4412578701582",
	) {
		t.Fatalf("stored distance = %q, err=%v", response, err)
	}
}

func TestLegacyGeoRadiusErrors(t *testing.T) {
	s := New(engine.New())
	seedLegacyGeoCities(t, s)

	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			"bad unit",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"200",
				"wat",
			),
			"ERR unsupported unit provided. please use M, KM, FT, MI",
		},
		{
			"bad radius",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"x",
				"km",
			),
			"ERR need numeric radius",
		},
		{
			"negative radius",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"-1",
				"km",
			),
			"ERR radius cannot be negative",
		},
		{
			"count zero",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"200",
				"km",
				"COUNT",
				"0",
			),
			"ERR COUNT must be > 0",
		},
		{
			"any without count",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"200",
				"km",
				"ANY",
			),
			"ERR the ANY argument requires COUNT argument",
		},
		{
			"store with enrich",
			geoArgs(
				"GEORADIUS",
				"geo:src",
				"15",
				"37",
				"200",
				"km",
				"WITHDIST",
				"STORE",
				"geo:store",
			),
			"ERR STORE option in GEORADIUS is not compatible with WITHDIST, WITHHASH and WITHCOORD options",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Execute(tc.args)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLegacyGeoRadiusCommandKeys(t *testing.T) {
	refs, err := commandKeys(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"200",
		"km",
		"STOREDIST",
		"geo:dist",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}
	if string(refs[0].value) != "geo:src" ||
		string(refs[1].value) != "geo:dist" {
		t.Fatalf("refs = %#v", refs)
	}
}


func TestLegacyGeoRadiusLastStoreOptionWins(t *testing.T) {
	s := New(engine.New())
	seedLegacyGeoCities(t, s)

	response, err := s.Execute(geoArgs(
		"GEORADIUS",
		"geo:src",
		"15",
		"37",
		"200",
		"km",
		"STORE",
		"geo:store2",
		"STOREDIST",
		"geo:dist2",
	))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("dual STORE/STOREDIST = %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs(
		"EXISTS",
		"geo:store2",
	))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("earlier STORE destination exists: %q, err=%v", response, err)
	}

	response, err = s.Execute(geoArgs(
		"ZRANGE",
		"geo:dist2",
		"0",
		"-1",
		"WITHSCORES",
	))
	if err != nil {
		t.Fatal(err)
	}

	got := string(response)
	for _, want := range []string{
		"Catania",
		"Palermo",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dual STOREDIST missing %q in %q", want, got)
		}
	}

	cataniaScore, found, err := s.store.ZSetScore(
		"geo:dist2",
		[]byte("Catania"),
	)
	if err != nil || !found ||
		math.Abs(cataniaScore-56.4412578701582) > 1e-12 {
		t.Fatalf(
			"Catania STOREDIST score = %.17g, found=%v, err=%v",
			cataniaScore,
			found,
			err,
		)
	}

	palermoScore, found, err := s.store.ZSetScore(
		"geo:dist2",
		[]byte("Palermo"),
	)
	if err != nil || !found ||
		math.Abs(palermoScore-190.44242984775784) > 1e-12 {
		t.Fatalf(
			"Palermo STOREDIST score = %.17g, found=%v, err=%v",
			palermoScore,
			found,
			err,
		)
	}
}
