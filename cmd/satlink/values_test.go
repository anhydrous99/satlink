package main

import (
	"slices"
	"testing"
)

func TestTextGivenIsToldFromNone(t *testing.T) {
	var port givenText

	if err := port.Set(""); err != nil || port != (givenText{given: true}) {
		t.Errorf("Set(\"\") = %v, %+v, want an empty text given", err, port)
	}
}

func TestEveryTextGivenIsKeptInOrder(t *testing.T) {
	var rules []string
	value := stringsValue{target: &rules}

	for _, rule := range []string{"a:1", "b:2"} {
		if err := value.Set(rule); err != nil {
			t.Fatal(err)
		}
	}

	if !slices.Equal(rules, []string{"a:1", "b:2"}) {
		t.Errorf("rules %q, want [a:1 b:2]", rules)
	}
}

func TestACountIsKeptAsGiven(t *testing.T) {
	var count int
	value := countValue{target: &count}

	if err := value.Set("3"); err != nil || count != 3 {
		t.Errorf("Set(3) = %v, count %d", err, count)
	}
	if err := value.Set("many"); err == nil || count != 3 {
		t.Errorf("Set(many) = %v, count %d; want it refused", err, count)
	}
}
