package matrixbot

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Prompter abstracts how RunInit / RunLogin ask the operator for input.
// Tests inject a canned implementation; the real one talks to stdin/stdout
// via NewStdioPrompter.
//
// secret==true means a password-style no-echo read. defaultVal, if
// non-empty and the operator just hits Enter, is what the prompter
// returns.
type Prompter interface {
	Prompt(label, defaultVal string, secret bool) (string, error)
}

// NewStdioPrompter returns a Prompter that reads from in (must be a
// terminal for the secret path) and prints labels to out. Returns an
// error if in is not a terminal — silently echoing a password would be a
// security footgun, and silently hanging on a closed stdin would leave
// the operator confused.
func NewStdioPrompter(in *os.File, out io.Writer) (Prompter, error) {
	if !term.IsTerminal(int(in.Fd())) {
		return nil, fmt.Errorf("stdin is not a terminal; interactive input is required")
	}
	return &stdioPrompter{in: in, out: out, reader: bufio.NewReader(in)}, nil
}

type stdioPrompter struct {
	in     *os.File
	out    io.Writer
	reader *bufio.Reader
}

func (p *stdioPrompter) Prompt(label, defaultVal string, secret bool) (string, error) {
	if secret {
		fmt.Fprintf(p.out, "%s: ", label)
		buf, err := term.ReadPassword(int(p.in.Fd()))
		// ReadPassword consumes the Enter without echoing a newline; print
		// one ourselves so the next prompt doesn't share its line.
		fmt.Fprintln(p.out)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", label, err)
		}
		return string(buf), nil
	}
	if defaultVal != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, defaultVal)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}
