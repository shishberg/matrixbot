package matrixbot

import "strings"

// scrubSecret replaces secret with "***" in msg. Defence in depth:
// homeserver client errors shouldn't echo the password or token back, but
// if they ever do, we MUST NOT print them on the operator's terminal
// where they'd land in scrollback or screen-share.
func scrubSecret(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "***")
}
