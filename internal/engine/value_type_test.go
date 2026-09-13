package engine

import (
	"bytes"
	"testing"
)

func TestClassifyValueCanonicalInt64(t *testing.T) {
	tests := []struct {
		value string
		want  ValueType
	}{
		{"0", TypeInt64},
		{"1", TypeInt64},
		{"-1", TypeInt64},
		{"9223372036854775807", TypeInt64},
		{"-9223372036854775808", TypeInt64},

		// Non-canonical textual representations must remain strings.
		{"00123", TypeString},
		{"+123", TypeString},
		{"-0", TypeFloat64},
		{"00", TypeString},

		// Outside signed 64-bit range is not INT64.
		{"9223372036854775808", TypeUint64},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := classifyValue([]byte(tt.value))

			if got != tt.want {
				t.Fatalf(
					"classifyValue(%q) = %s, want %s",
					tt.value,
					got.String(),
					tt.want.String(),
				)
			}
		})
	}
}

func TestClassifyValueBytes(t *testing.T) {
	value := []byte{0xff, 0xfe, 0xfd}

	if got := classifyValue(value); got != TypeBytes {
		t.Fatalf("got %s, want BYTES", got.String())
	}
}

func TestClassifyValueUTF8String(t *testing.T) {
	values := [][]byte{
		[]byte("hello"),
		[]byte(""),
		[]byte("Latvija"),
		[]byte("こんにちは"),
		[]byte("😀"),
	}

	for _, value := range values {
		if got := classifyValue(value); got != TypeString {
			t.Fatalf(
				"classifyValue(%q) = %s, want STRING",
				value,
				got.String(),
			)
		}
	}
}

func TestValueTypeDoesNotChangeRoundTrip(t *testing.T) {
	store := New()

	values := [][]byte{
		[]byte("123"),
		[]byte("00123"),
		[]byte("-9223372036854775808"),
		[]byte("normal string"),
		[]byte{0x00, 0xff, 0x01, 0xfe},
	}

	for i, value := range values {
		key := string(rune('a' + i))

		if err := store.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}

		got, ok := store.Get(key)
		if !ok {
			t.Fatalf("missing key %q", key)
		}

		if !bytes.Equal(got, value) {
			t.Fatalf(
				"round trip changed bytes for %q: got %v want %v",
				key,
				got,
				value,
			)
		}
	}
}

func TestValueTypeOf(t *testing.T) {
	store := New()

	if err := store.Set("integer", []byte("123"), 0); err != nil {
		t.Fatal(err)
	}

	if err := store.Set("string", []byte("00123"), 0); err != nil {
		t.Fatal(err)
	}

	if err := store.Set(
		"bytes",
		[]byte{0xff, 0xfe},
		0,
	); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key  string
		want ValueType
	}{
		{"integer", TypeInt64},
		{"string", TypeString},
		{"bytes", TypeBytes},
	}

	for _, tt := range tests {
		got, ok := store.ValueTypeOf(tt.key)

		if !ok {
			t.Fatalf("missing key %q", tt.key)
		}

		if got != tt.want {
			t.Fatalf(
				"%s type = %s, want %s",
				tt.key,
				got.String(),
				tt.want.String(),
			)
		}
	}
}

func TestClassifyValueUint64(t *testing.T) {
	tests := []struct {
		value string
		want  ValueType
	}{
		{"9223372036854775807", TypeInt64},
		{"9223372036854775808", TypeUint64},
		{"18446744073709551615", TypeUint64},

		// Outside uint64.
		{"18446744073709551616", TypeString},

		// Non-canonical.
		{"09223372036854775808", TypeString},
		{"+9223372036854775808", TypeString},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := classifyValue([]byte(tt.value))

			if got != tt.want {
				t.Fatalf(
					"classifyValue(%q) = %s, want %s",
					tt.value,
					got.String(),
					tt.want.String(),
				)
			}
		})
	}
}

func TestUint64StoreRoundTrip(t *testing.T) {
	store := New()

	value := []byte("18446744073709551615")

	if err := store.Set("uint", value, 0); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Get("uint")
	if !ok {
		t.Fatal("missing uint")
	}

	if !bytes.Equal(got, value) {
		t.Fatalf(
			"round trip changed value: got %q want %q",
			got,
			value,
		)
	}

	valueType, ok := store.ValueTypeOf("uint")
	if !ok {
		t.Fatal("missing uint type")
	}

	if valueType != TypeUint64 {
		t.Fatalf(
			"type = %s, want UINT64",
			valueType.String(),
		)
	}
}

func TestClassifyValueFloat64(t *testing.T) {
	tests := []struct {
		value string
		want  ValueType
	}{
		{"0.1", TypeFloat64},
		{"-0.1", TypeFloat64},
		{"3.141592653589793", TypeFloat64},
		{"1e+20", TypeFloat64},
		{"1e-20", TypeFloat64},
		{"-0", TypeFloat64},

		// Integers retain their more specific types.
		{"0", TypeInt64},
		{"123", TypeInt64},
		{"9223372036854775808", TypeUint64},

		// Valid numeric values whose textual representation would change
		// during reconstruction remain strings.
		{"1.0", TypeString},
		{"1.00", TypeString},
		{"01.5", TypeString},
		{"+1.5", TypeString},
		{"1e3", TypeString},
		{"1E+20", TypeString},

		// Special IEEE-754 values are not inferred as native FLOAT64.
		{"NaN", TypeString},
		{"Inf", TypeString},
		{"+Inf", TypeString},
		{"-Inf", TypeString},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := classifyValue([]byte(tt.value))
			if got != tt.want {
				t.Fatalf(
					"classifyValue(%q) = %s, want %s",
					tt.value,
					got.String(),
					tt.want.String(),
				)
			}
		})
	}
}

func TestFloat64StoreRoundTrip(t *testing.T) {
	store := New()

	values := []string{
		"0.1",
		"-0.1",
		"3.141592653589793",
		"1e+20",
		"1e-20",
		"-0",
	}

	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			key := "float:" + value

			if err := store.Set(key, []byte(value), 0); err != nil {
				t.Fatal(err)
			}

			got, ok := store.Get(key)
			if !ok {
				t.Fatal("missing float")
			}

			if !bytes.Equal(got, []byte(value)) {
				t.Fatalf(
					"round trip changed value: got %q want %q",
					got,
					value,
				)
			}

			valueType, ok := store.ValueTypeOf(key)
			if !ok {
				t.Fatal("missing float type")
			}

			if valueType != TypeFloat64 {
				t.Fatalf(
					"type = %s, want FLOAT64",
					valueType.String(),
				)
			}
		})
	}
}
