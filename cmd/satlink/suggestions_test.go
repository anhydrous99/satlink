package main

import (
	"math"
	"slices"
	"testing"
)

func TestJaroSimilarityIsStrsims(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{
		{"", "", 1},
		{"a", "", 0},
		{"abc", "xyz", 0},
		{"same", "same", 1},
		// The classic examples of Jaro's paper.
		{"MARTHA", "MARHTA", 0.944},
		{"DWAYNE", "DUANE", 0.822},
		{"DIXON", "DICKSONX", 0.767},
		// Why clap suggested 'list' for "--simulate instant".
		{"instant", "list", 0.726},
		{"ver", "verbose", 0.810},
		{"ver", "version", 0.810},
	}
	for _, test := range tests {
		if got := jaro(test.a, test.b); math.Abs(got-test.want) > 0.001 {
			t.Errorf("jaro(%q, %q) = %.3f, want %.3f", test.a, test.b, got, test.want)
		}
	}
}

func TestSimilarNamesComeLeastSimilarFirstAndKeepTheirOrderWhenAlike(t *testing.T) {
	commands := []string{"list", "info", "status", "sms", "dial", "answer", "hangup"}

	got := similarNames("s", commands)

	if want := []string{"status", "answer", "sms"}; !slices.Equal(got, want) {
		t.Errorf("similarNames = %q, want %q", got, want)
	}
}

func TestTheMostSimilarNameIsTheLastOfEquals(t *testing.T) {
	if got, found := mostSimilarName("ver", []string{"verbose", "version"}); !found || got != "version" {
		t.Errorf("mostSimilarName = %q, %t, want version", got, found)
	}
	if got, found := mostSimilarName("frob", []string{"verbose", "version"}); found {
		t.Errorf("mostSimilarName found %q for frob", got)
	}
}
