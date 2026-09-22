package jsonvalue

import (
	"reflect"
	"testing"
)

func TestGetNested(t *testing.T) {
	root, err := Parse([]byte(`{
		"user": {
			"name": "Edijs"
		}
	}`))

	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "$.user.name")

	if err != nil {
		t.Fatal(err)
	}

	if !found {
		t.Fatal("path not found")
	}

	if value != "Edijs" {
		t.Fatalf("got %#v", value)
	}
}

func TestSetNested(t *testing.T) {
	root, err := Parse([]byte(`{
		"user": {
			"name": "old"
		}
	}`))

	if err != nil {
		t.Fatal(err)
	}

	root, err = Set(root, "$.user.name", "Edijs")
	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "$.user.name")

	if err != nil || !found {
		t.Fatal("path not found")
	}

	if value != "Edijs" {
		t.Fatalf("got %#v", value)
	}
}


func TestExactJSONPathArraysAndBracketMembers(t *testing.T) {
	root, err := Parse([]byte(`{"user":{"name":"Edijs","weird.key":"dot"},"items":[{"name":"a"},{"name":"b"},3]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		want any
	}{
		{"$.items[0].name", "a"},
		{"$.items[-1]", float64(3)},
		{`$["user"]["name"]`, "Edijs"},
		{`$["user"]["weird.key"]`, "dot"},
	} {
		got, found, err := Get(root, tc.path)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", tc.path, found, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestExactJSONPathSetAndDeleteArray(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"name":"a"},{"name":"b"},{"name":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, err = Set(root, "$.items[1].name", "Bee")
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := Get(root, "$.items[1].name")
	if err != nil || !found || value != "Bee" {
		t.Fatalf("set result: %#v %v %v", value, found, err)
	}

	root, deleted, err := Delete(root, "$.items[1]")
	if err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	encoded, err := Encode(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"items":[{"name":"a"},{"name":"c"}]}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestExactJSONPathInvalidSyntax(t *testing.T) {
	root, err := Parse([]byte(`{"a":[1]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"", "$.", "$..", "$...[a]", "$[", "$.a[", "$.a[]", "$.a[x]"} {
		if _, _, err := Get(root, path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
}


func TestJSONPathWildcardMatchesAndMutates(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"score":1},{"score":2},{"score":3}],"obj":{"a":1,"b":2}}`))
	if err != nil {
		t.Fatal(err)
	}

	values, err := Matches(root, "$.items[*].score")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(1), float64(2), float64(3)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("matches got %#v want %#v", values, want)
	}

	root, count, err := SetMatches(root, "$.items[*].score", float64(9))
	if err != nil || count != 3 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$.items[*].score")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(9), float64(9), float64(9)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("updated matches got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$.obj.*")
	if err != nil || count != 2 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}
	value, found, err := Get(root, ".obj")
	if err != nil || !found {
		t.Fatalf("legacy get found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(value, map[string]any{}) {
		t.Fatalf("object after wildcard delete: %#v", value)
	}
}

func TestLegacyPathWithoutLeadingDot(t *testing.T) {
	root, err := Parse([]byte(`{"a":{"b":2}}`))
	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "a.b")
	if err != nil || !found || value != float64(2) {
		t.Fatalf("got %#v found=%v err=%v", value, found, err)
	}
}


func TestJSONPathRecursiveDescent(t *testing.T) {
	root, err := Parse([]byte(`{
		"name":"root",
		"nested":{"name":"child","deep":{"name":"leaf"}},
		"users":[{"id":1,"profile":{"id":10}},{"id":2}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	values, err := Matches(root, "$..name")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"root", "child", "leaf"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("$..name got %#v want %#v", values, want)
	}

	values, err = Matches(root, "$.users..id")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(1), float64(10), float64(2)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("$.users..id got %#v want %#v", values, want)
	}
}

func TestJSONPathRecursiveSetAndDelete(t *testing.T) {
	root, err := Parse([]byte(`{
		"a":{"score":1,"nested":{"score":2}},
		"b":[{"score":3},{"x":1}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$..score", float64(9))
	if err != nil || count != 3 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err := Matches(root, "$..score")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(9), float64(9), float64(9)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("scores got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$..score")
	if err != nil || count != 3 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$..score")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("scores remain: %#v", values)
	}
}


func TestJSONPathSlicesAndUnions(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2,3,4,5]}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[1:4]", []any{float64(1), float64(2), float64(3)}},
		{"$.items[:3]", []any{float64(0), float64(1), float64(2)}},
		{"$.items[::2]", []any{float64(0), float64(2), float64(4)}},
		{"$.items[-3:]", []any{float64(3), float64(4), float64(5)}},
		{"$.items[0,2,4]", []any{float64(0), float64(2), float64(4)}},
		{"$.items[-1,0,-1]", []any{float64(5), float64(0)}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathSliceAndUnionMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2,3,4,5]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[1:5:2]", float64(9))
	if err != nil || count != 2 {
		t.Fatalf("set count=%d err=%v", count, err)
	}
	values, err := Matches(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(0), float64(9), float64(2), float64(9), float64(4), float64(5)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after set got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$.items[0,2,4]")
	if err != nil || count != 3 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}
	values, err = Matches(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(9), float64(9), float64(5)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after delete got %#v want %#v", values, want)
	}
}

func TestJSONPathSliceRejectsZeroStep(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Matches(root, "$.items[::0]"); err == nil {
		t.Fatal("expected zero-step slice to fail")
	}
}


func TestJSONPathScalarFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"price":50,"name":"cheap","active":true,"meta":{"score":2}},
			{"price":100,"name":"mid","active":false,"meta":{"score":5}},
			{"price":150,"name":"expensive","active":true,"meta":{"score":9}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?(@.price < 100)].name", []any{"cheap"}},
		{"$.items[?(@.price >= 100)].name", []any{"mid", "expensive"}},
		{"$.items[?(@.name == \"mid\")].price", []any{float64(100)}},
		{"$.items[?(@.name != \"mid\")].name", []any{"cheap", "expensive"}},
		{"$.items[?(@.active == true)].name", []any{"cheap", "expensive"}},
		{"$.items[?(@.meta.score > 2)].name", []any{"mid", "expensive"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathFilterMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"price":50},{"price":100},{"price":150}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?(@.price >= 100)].price", float64(999))
	if err != nil || count != 2 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err := Matches(root, "$.items[*].price")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(50), float64(999), float64(999)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after set got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$.items[?(@.price == 999)]")
	if err != nil || count != 2 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$.items[*].price")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(50)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after delete got %#v want %#v", values, want)
	}
}

func TestJSONPathFilterRejectsInvalidExpressions(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"price":1}]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"$.items[?(@.price)]",
		"$.items[?(@.price <> 1)]",
		"$.items[?($.price < 1)]",
		"$.items[?(@.price <)]",
	} {
		if _, err := Matches(root, path); err == nil {
			t.Fatalf("accepted invalid filter %q", path)
		}
	}
}


func TestJSONPathLogicalFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"price":25,"active":true,"name":"a"},
			{"price":75,"active":false,"name":"b"},
			{"price":150,"active":true,"name":"c"},
			{"price":700,"active":false,"name":"d"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?(@.price < 100 && @.active == true)].name", []any{"a"}},
		{"$.items[?(@.price < 50 || @.price > 500)].name", []any{"a", "d"}},
		{"$.items[?(!(@.active == true))].name", []any{"b", "d"}},
		{"$.items[?((@.price < 100 && @.active == false) || @.price > 500)].name", []any{"b", "d"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathLogicalFilterMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"price":25,"active":true},{"price":75,"active":false},{"price":700,"active":false}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?(@.price < 100 && @.active == false)].price", float64(999))
	if err != nil || count != 1 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	root, count, err = DeleteMatches(root, "$.items[?(@.price < 50 || @.price > 500)]")
	if err != nil || count != 3 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}

	values, err := Matches(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("expected all matching items deleted, got %#v", values)
	}
}


func TestJSONPathRegexAndMembershipFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"name":"alpha","kind":"a","tags":["x","y"],"allowed":["a","b"]},
			{"name":"beta","kind":"c","tags":["z"],"allowed":["a","b"]},
			{"name":"ALLOY","kind":"b","tags":["x"],"allowed":["b","c"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?(@.name =~ \"^a\")].name", []any{"alpha"}},
		{"$.items[?(@.name =~ \"(?i)al\")].name", []any{"alpha", "ALLOY"}},
		{"$.items[?(@.kind in [\"a\",\"b\"])].name", []any{"alpha", "ALLOY"}},
		{"$.items[?(@.kind nin [\"a\",\"b\"])].name", []any{"beta"}},
		{"$.items[?(@.kind in @.allowed)].name", []any{"alpha", "ALLOY"}},
		{"$.items[?(@.kind nin @.allowed)].name", []any{"beta"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathRegexAndMembershipComposeWithLogic(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"name":"alpha","kind":"a"},{"name":"beta","kind":"c"},{"name":"ALLOY","kind":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	got, err := Matches(root, `$.items[?(@.name =~ "(?i)^a" && @.kind in ["a","b"])].name`)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"alpha", "ALLOY"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}


func TestJSONPathSetRelationSizeAndEmptyFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"sets":[
			{"vals":[1,2],"allow":[1,2,3],"name":"subset"},
			{"vals":[1,5],"allow":[1,2,3],"name":"overlap"},
			{"vals":[8,9],"allow":[1,2,3],"name":"disjoint"},
			{"vals":[],"allow":[1,2,3],"name":"empty"}
		],
		"mixed":[[],[1],"",[2,3],{},{"k":1},"åä"]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.sets[?(@.vals subsetof @.allow)].name", []any{"subset", "empty"}},
		{"$.sets[?(@.vals anyof @.allow)].name", []any{"subset", "overlap"}},
		{"$.sets[?(@.vals noneof @.allow)].name", []any{"disjoint", "empty"}},
		{"$.mixed[?(@ sizeof 0)]", []any{[]any{}, "", map[string]any{}}},
		{"$.mixed[?(@ size 2)]", []any{[]any{float64(2), float64(3)}, "åä"}},
		{"$.mixed[?(@ empty true)]", []any{[]any{}, "", map[string]any{}}},
		{"$.mixed[?(@ empty false)]", []any{[]any{float64(1)}, []any{float64(2), float64(3)}, map[string]any{"k": float64(1)}, "åä"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathSetRelationAndSizeMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"tags":["a"]},{"tags":["a","b"]},{"tags":[]},{"tags":["z"]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, `$.items[?(@.tags subsetof ["a","b"])].matched`, true)
	if err != nil || count != 3 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err := Matches(root, "$.items[?(@.matched == true)].tags")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("matched values=%#v", values)
	}

	root, count, err = DeleteMatches(root, "$.items[?(@.tags empty true)]")
	if err != nil || count != 1 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$.items[*].tags")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("remaining values=%#v", values)
	}
}


func TestJSONPathArithmeticFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"a":2,"b":3,"name":"x"},
			{"a":5,"b":2,"name":"y"},
			{"a":9,"b":0,"name":"z"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?(@.a + 1 == 3)].name", []any{"x"}},
		{"$.items[?(@.a + @.b * 2 == 8)].name", []any{"x"}},
		{"$.items[?((@.a + @.b) * 2 == 10)].name", []any{"x"}},
		{"$.items[?(-@.a == -5)].name", []any{"y"}},
		{"$.items[?(+@.a == 9)].name", []any{"z"}},
		{"$.items[?(@.a / @.b > 2)].name", []any{"y"}},
		{"$.items[?(@.a % 2 == 1)].name", []any{"y", "z"}},
		{"$.items[?(@.a / @.b > 0)].name", []any{"x", "y"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathArithmeticFilterMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"price":10,"qty":2},{"price":30,"qty":4},{"price":5,"qty":50}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?(@.price * @.qty >= 100)].selected", true)
	if err != nil || count != 2 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	got, err := Matches(root, "$.items[?(@.selected == true)].price")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(30), float64(5)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}


func TestJSONPathLengthFunctionFilters(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"name":"a","tags":["x","y","z"],"meta":{"x":1,"y":2}},
			{"name":"bb","tags":["x"],"meta":{"x":1}},
			{"name":"ccc","tags":[],"meta":{}},
			{"name":"åä","tags":[1,2],"meta":{"x":1,"y":2,"z":3}},
			{"name":"skip","tags":42,"meta":null}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?length(@.tags) > 1].name", []any{"a", "åä"}},
		{"$.items[?(@.tags.length() == 1)].name", []any{"bb"}},
		{"$.items[?length(@.meta) == 0].name", []any{"ccc"}},
		{"$.items[?(@.name.length() == 2)].name", []any{"bb", "åä"}},
		{"$.items[?(length(@.tags) + 1 == 4)].name", []any{"a"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}


func TestJSONPathNumericFunctions(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"n":-5},{"n":5},{"n":2.1},{"n":2.9},{"n":3.5},{"n":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want int
	}{
		{"$.items[?abs(@.n) == 5]", 2},
		{"$.items[?(@.n.abs() == 5)]", 2},
		{"$.items[?ceiling(@.n) == 3]", 2},
		{"$.items[?(@.n.floor() == 2)]", 2},
		{"$.items[?(floor(abs(@.n)) == 5)]", 2},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if len(got) != tc.want {
			t.Fatalf("%s got %d matches want %d (%#v)", tc.path, len(got), tc.want, got)
		}
	}
}

func TestJSONPathNumericFunctionMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"n":-5},{"n":2.1},{"n":2.9}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?ceiling(@.n) == 3].hit", true)
	if err != nil || count != 2 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	got, err := Matches(root, "$.items[?(@.hit == true)].n")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(2.1), float64(2.9)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}


func TestJSONPathArrayAccessFunctions(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"n":[1,2,3],"name":"a"},
			{"n":[9,8],"name":"b"},
			{"n":[],"name":"c"},
			{"n":"no","name":"d"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?first(@.n) == 1].name", []any{"a"}},
		{"$.items[?(@.n.first() == 9)].name", []any{"b"}},
		{"$.items[?last(@.n) == 3].name", []any{"a"}},
		{"$.items[?(@.n.last() == 8)].name", []any{"b"}},
		{"$.items[?index(@.n, -1) == 3].name", []any{"a"}},
		{"$.items[?(@.n.index(-1) == 8)].name", []any{"b"}},
		{"$.items[?index(@.n, 1.9) == 2].name", []any{"a"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathArrayAccessFunctionMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"n":[1,2]},{"n":[9,8]},{"n":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?last(@.n) == 8].hit", true)
	if err != nil || count != 1 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	got, err := Matches(root, "$.items[?(@.hit == true)].n")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{[]any{float64(9), float64(8)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}


func TestJSONPathAggregationFunctions(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"n":[3,1,2],"name":"a"},
			{"n":[5,6],"name":"b"},
			{"n":[],"name":"empty"},
			{"n":[1,"x"],"name":"mixed"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?min(@.n) == 1].name", []any{"a"}},
		{"$.items[?(@.n.max() == 6)].name", []any{"b"}},
		{"$.items[?sum(@.n) == 6].name", []any{"a"}},
		{"$.items[?(@.n.avg() == 2)].name", []any{"a"}},
		{"$.items[?stddev(@.n) > 0.8 && stddev(@.n) < 0.82].name", []any{"a"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}

	if got, err := Matches(root, "$.items[?sum(@.n) > 0].name"); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Fatalf("strict sum got %#v", got)
	}
}

func TestJSONPathAggregationFunctionMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"n":[3,1,2]},{"n":[5,6]},{"n":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[?avg(@.n) >= 5].hit", true)
	if err != nil || count != 1 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	got, err := Matches(root, "$.items[?(@.hit == true)].n")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{[]any{float64(5), float64(6)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}


func TestJSONPathAppendFunction(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"n":[1,2],"name":"a"},
			{"n":[5],"name":"b"},
			{"n":"bad","name":"c"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?last(append(@.n, 9)) == 9].name", []any{"a", "b"}},
		{"$.items[?(@.n.append(7,8).length() == 4)].name", []any{"a"}},
		{"$.items[?index(append(@.n, [7,8]), -1) == [7,8]].name", []any{"a", "b"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}

	original, err := Matches(root, "$.items[0].n[*]")
	if err != nil {
		t.Fatal(err)
	}
	wantOriginal := []any{float64(1), float64(2)}
	if !reflect.DeepEqual(original, wantOriginal) {
		t.Fatalf("append mutated stored value: %#v", original)
	}
}


func TestJSONPathKeysFunction(t *testing.T) {
	root, err := Parse([]byte(`{
		"items":[
			{"meta":{"b":2,"a":1},"name":"x"},
			{"meta":{"z":1},"name":"y"},
			{"meta":[],"name":"bad"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[?keys(@.meta).length() == 2].name", []any{"x"}},
		{"$.items[?(@.meta.keys().length() == 1)].name", []any{"y"}},
		{"$.items[?first(keys(@.meta)) == \"a\"].name", []any{"x"}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}


func TestJSONPathNodeListFunctions(t *testing.T) {
	root, err := Parse([]byte(`[
		{"a":1,"b":2,"c":3},
		{"a":1},
		{"x":9},
		{}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$[?count(@.*) == 3].a", []any{float64(1)}},
		{"$[?count(@.missing) == 0].x", []any{float64(9)}},
		{"$[?value(@.a) == 1].a", []any{float64(1), float64(1)}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathValueRequiresExactlyOneNode(t *testing.T) {
	root, err := Parse([]byte(`[{"a":1,"b":2},{"a":1}]`))
	if err != nil {
		t.Fatal(err)
	}

	got, err := Matches(root, "$[?value(@.*) == 1]")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"a": float64(1)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("value() should skip multi-node input and accept exactly-one-node input: got %#v want %#v", got, want)
	}
}
