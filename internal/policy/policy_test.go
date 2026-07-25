package policy

import (
	"strings"
	"testing"
)

func TestInstructions(t *testing.T) {
	got := Instructions()
	if got == "" {
		t.Fatal("Instructions() is empty")
	}
	if got != strings.TrimSpace(got) {
		t.Error("Instructions() is not trimmed")
	}
	for _, want := range []string{"絶対ルール", "メモリ追記"} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions() does not contain %q", want)
		}
	}
}
