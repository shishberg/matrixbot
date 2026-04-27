package matrixbot

import "testing"

// canned answers for unit tests; the real prompter lives in
// prompter.go and is exercised manually via the init subcommand.
type cannedPrompter struct {
	answers map[string]string
	calls   []string
}

func (c *cannedPrompter) Prompt(label, defaultVal string, secret bool) (string, error) {
	c.calls = append(c.calls, label)
	if v, ok := c.answers[label]; ok {
		return v, nil
	}
	return defaultVal, nil
}

func TestCannedPrompterRecordsCallsAndReturnsAnswers(t *testing.T) {
	p := &cannedPrompter{answers: map[string]string{"hostname": "foo"}}
	got, err := p.Prompt("hostname", "default", false)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got != "foo" {
		t.Errorf("got %q, want %q", got, "foo")
	}
	if len(p.calls) != 1 || p.calls[0] != "hostname" {
		t.Errorf("calls = %v, want [hostname]", p.calls)
	}
}
