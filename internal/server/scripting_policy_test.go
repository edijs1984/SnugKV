package server

import "testing"

func TestScriptCommandWritesOrReplicatesPFCount(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want bool
	}{
		{
			name: "PFCOUNT",
			args: [][]byte{[]byte("PFCOUNT"), []byte("hll")},
			want: true,
		},
		{
			name: "PUBLISH",
			args: [][]byte{[]byte("PUBLISH"), []byte("channel"), []byte("value")},
			want: true,
		},
		{
			name: "SPUBLISH",
			args: [][]byte{[]byte("SPUBLISH"), []byte("channel"), []byte("value")},
			want: true,
		},
		{
			name: "GET",
			args: [][]byte{[]byte("GET"), []byte("key")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scriptCommandWritesOrReplicates(tt.args)
			if got != tt.want {
				t.Fatalf(
					"scriptCommandWritesOrReplicates(%q) = %v, want %v",
					tt.args[0],
					got,
					tt.want,
				)
			}
		})
	}
}

func TestScriptCommandForbiddenNoscriptFamilies(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
	}{
		{
			name: "CLIENT",
			args: [][]byte{[]byte("CLIENT"), []byte("ID")},
		},
		{
			name: "CONFIG",
			args: [][]byte{[]byte("CONFIG"), []byte("GET"), []byte("maxmemory")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !scriptCommandForbidden(tt.args) {
				t.Fatalf(
					"scriptCommandForbidden(%q) = false, want true",
					tt.args[0],
				)
			}
		})
	}
}
