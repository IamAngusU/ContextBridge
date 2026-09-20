package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitWorkerList(t *testing.T) {
	got := splitWorkerList(" adapter, ollama ,, ")
	if !reflect.DeepEqual(got, []string{"adapter", "ollama"}) {
		t.Fatalf("unexpected worker policy list: %#v", got)
	}
}

func TestSessionSlotOverrideRejectsUnsafeRange(t *testing.T) {
	for _, args := range [][]string{{"--slots", "65"}, {"--slots", "-1"}} {
		if err := runCommand(args); err == nil || !strings.Contains(err.Error(), "--slots") {
			t.Fatalf("run accepted invalid slots %v: %v", args, err)
		}
		if err := workerCommand(args); err == nil || !strings.Contains(err.Error(), "--slots") {
			t.Fatalf("worker accepted invalid slots %v: %v", args, err)
		}
	}
}
