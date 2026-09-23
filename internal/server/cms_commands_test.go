package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func cmsArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestCMSOracleCore(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(cmsArgs("CMS.INITBYDIM", "cms", "20", "5"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("INITBYDIM=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.QUERY", "cms", "apple", "banana"))
	if err != nil || string(response) != "*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("QUERY empty=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.INCRBY", "cms", "apple", "3", "banana", "2", "apple", "4"))
	if err != nil || string(response) != "*3\r\n:3\r\n:2\r\n:7\r\n" {
		t.Fatalf("INCRBY=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.QUERY", "cms", "apple", "banana", "missing"))
	if err != nil || string(response) != "*3\r\n:7\r\n:2\r\n:0\r\n" {
		t.Fatalf("QUERY=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.INFO", "cms"))
	if err != nil || string(response) != "*6\r\n+width\r\n:20\r\n+depth\r\n:5\r\n+count\r\n:9\r\n" {
		t.Fatalf("INFO=%q err=%v", response, err)
	}

	response, err = s.Execute(cmsArgs("CMS.INITBYPROB", "prob", "0.01", "0.01"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("INITBYPROB=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.INFO", "prob"))
	if err != nil || string(response) != "*6\r\n+width\r\n:200\r\n+depth\r\n:7\r\n+count\r\n:0\r\n" {
		t.Fatalf("prob INFO=%q err=%v", response, err)
	}
}

func TestCMSOracleMerge(t *testing.T) {
	s := New(engine.New())
	for _, key := range []string{"a", "b", "merged", "weighted"} {
		if response, err := s.Execute(cmsArgs("CMS.INITBYDIM", key, "20", "5")); err != nil || string(response) != "+OK\r\n" {
			t.Fatalf("init %s=%q err=%v", key, response, err)
		}
	}
	if _, err := s.Execute(cmsArgs("CMS.INCRBY", "a", "x", "2", "y", "3")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INCRBY", "b", "x", "5", "z", "7")); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(cmsArgs("CMS.MERGE", "merged", "2", "a", "b"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("merge=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.QUERY", "merged", "x", "y", "z"))
	if err != nil || string(response) != "*3\r\n:7\r\n:3\r\n:7\r\n" {
		t.Fatalf("merged query=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.MERGE", "weighted", "2", "a", "b", "WEIGHTS", "2", "3"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("weighted merge=%q err=%v", response, err)
	}
	response, err = s.Execute(cmsArgs("CMS.QUERY", "weighted", "x", "y", "z"))
	if err != nil || string(response) != "*3\r\n:19\r\n:6\r\n:21\r\n" {
		t.Fatalf("weighted query=%q err=%v", response, err)
	}
}

func TestCMSOracleErrors(t *testing.T) {
	s := New(engine.New())

	for _, args := range [][]string{
		{"CMS.QUERY", "missing", "x"},
		{"CMS.INFO", "missing"},
		{"CMS.MERGE", "badmerge", "2", "a"},
		{"CMS.MERGE", "badweights", "2", "a", "b", "WEIGHTS", "1"},
	} {
		if _, err := s.Execute(cmsArgs(args...)); err == nil || err.Error() != "CMS: key does not exist" {
			t.Fatalf("%v err=%v", args, err)
		}
	}

	if _, err := s.Execute(cmsArgs("CMS.INITBYDIM", "badw", "0", "5")); err == nil || err.Error() != "CMS: invalid width" {
		t.Fatalf("bad width=%v", err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INITBYDIM", "badd", "20", "0")); err == nil || err.Error() != "CMS: invalid depth" {
		t.Fatalf("bad depth=%v", err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INITBYPROB", "p1", "0", "0.01")); err == nil || err.Error() != "CMS: invalid overestimation value" {
		t.Fatalf("bad overest=%v", err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INITBYPROB", "p2", "0.01", "0")); err == nil || err.Error() != "CMS: invalid prob value" {
		t.Fatalf("bad prob=%v", err)
	}

	if _, err := s.Execute(cmsArgs("CMS.INITBYDIM", "neg", "20", "5")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INCRBY", "neg", "x", "3")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(cmsArgs("CMS.INCRBY", "neg", "x", "-2")); err == nil || err.Error() != "CMS: Number cannot be negative" {
		t.Fatalf("negative increment=%v", err)
	}

	if _, err := s.Execute(cmsArgs("SET", "plain", "value")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"CMS.QUERY", "plain", "x"},
		{"CMS.INCRBY", "plain", "x", "1"},
		{"CMS.INFO", "plain"},
	} {
		if _, err := s.Execute(cmsArgs(args...)); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
			t.Fatalf("%v err=%v", args, err)
		}
	}

	if got := string(errorResponse(errors.New("CMS: key does not exist"))); got != "-CMS: key does not exist\r\n" {
		t.Fatalf("wire CMS error=%q", got)
	}
}
