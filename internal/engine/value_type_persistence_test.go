package engine

import (
	"bytes"
	"snugkv/internal/persistence"
	"testing"
)

func TestNativeValueTypesSurviveExportRestore(t *testing.T) {
	source, err := NewWithOptions(Options{
		Shards:      16,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key   string
		value []byte
		typ   ValueType
	}{
		{"string", []byte("hello world"), TypeString},
		{"int64", []byte("-9223372036854775808"), TypeInt64},
		{"uint64", []byte("18446744073709551615"), TypeUint64},
		{"float64", []byte("3.141592653589793"), TypeFloat64},
		{"bool", []byte("true"), TypeBool},
		{"bytes", []byte{0xff, 0xfe, 0xfd, 0x00, 0x01}, TypeBytes},
	}

	for _, tt := range tests {
		if err := source.Set(tt.key, tt.value, 0); err != nil {
			t.Fatalf("%s SET: %v", tt.key, err)
		}
	}

	ok, err := source.JSONSet(
		"json",
		"$",
		[]byte(`{"name":"SnugKV","enabled":true,"count":123}`),
		false,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("JSON.SET not applied")
	}

	sourceJSON, found, err := source.JSONGet("json", "$")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("source JSON value missing")
	}

	records := source.Export(nil)

	if len(records) != 7 {
		t.Fatalf("exported records = %d, want 7", len(records))
	}

	expectedTypes := map[string]ValueType{
		"string":  TypeString,
		"int64":   TypeInt64,
		"uint64":  TypeUint64,
		"float64": TypeFloat64,
		"bool":    TypeBool,
		"bytes":   TypeBytes,
		"json":    TypeJSON,
	}

	for _, record := range records {
		want, ok := expectedTypes[string(record.Key)]
		if !ok {
			t.Fatalf("unexpected exported key %q", record.Key)
		}

		if ValueType(record.ValueType) != want {
			t.Fatalf(
				"%s persisted type = %d (%s), want %d (%s)",
				record.Key,
				record.ValueType,
				ValueType(record.ValueType).String(),
				want,
				want.String(),
			)
		}
	}

	target, err := NewWithOptions(Options{
		Shards:      16,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := target.Restore(records, false); err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, found := target.Get(tt.key)
			if !found {
				t.Fatalf("%s missing after restore", tt.key)
			}

			if !bytes.Equal(got, tt.value) {
				t.Fatalf(
					"%s bytes changed: got %v want %v",
					tt.key,
					got,
					tt.value,
				)
			}

			gotType, found := target.ValueTypeOf(tt.key)
			if !found {
				t.Fatalf("%s type missing after restore", tt.key)
			}

			if gotType != tt.typ {
				t.Fatalf(
					"%s type = %s, want %s",
					tt.key,
					gotType.String(),
					tt.typ.String(),
				)
			}
		})
	}

	jsonType, found := target.ValueTypeOf("json")
	if !found {
		t.Fatal("JSON key missing after restore")
	}
	if jsonType != TypeJSON {
		t.Fatalf("JSON type = %s, want JSON", jsonType.String())
	}

	jsonValue, found, err := target.JSONGet("json", "$")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("JSON value missing after restore")
	}

	if !bytes.Equal(jsonValue, sourceJSON) {
		t.Fatalf(
			"JSON bytes changed across persistence: got %q want %q",
			jsonValue,
			sourceJSON,
		)
	}
}

func TestRestoreRejectsUnknownSemanticType(t *testing.T) {
	store := New()

	records := []persistence.Record{
		{
			Key:       []byte("bad"),
			Value:     []byte("value"),
			ValueType: 255,
		},
	}

	if err := store.Restore(records, false); err == nil {
		t.Fatal("expected restore to reject unknown semantic type")
	}
}
