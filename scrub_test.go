package matrixbot

import "testing"

func TestScrubSecretReplacesAllOccurrences(t *testing.T) {
	got := scrubSecret("login failed: hunter2 / hunter2", "hunter2")
	want := "login failed: *** / ***"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScrubSecretEmptySecretIsNoop(t *testing.T) {
	got := scrubSecret("anything", "")
	if got != "anything" {
		t.Errorf("got %q, want unchanged", got)
	}
}
