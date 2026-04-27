package e2ee

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// TestBootstrapNoArgsIsNoOp covers the "feature disabled" path: when neither a
// password nor a recovery key is configured, Bootstrap must not touch the
// OlmMachine and must return ("", nil). Passing a nil machine is the cheapest
// way to prove that contract — if the function tried to use it the test would
// panic.
func TestBootstrapNoArgsIsNoOp(t *testing.T) {
	got, err := Bootstrap(context.Background(), nil, "", "")
	if err != nil {
		t.Fatalf("Bootstrap with empty creds: %v", err)
	}
	if got != "" {
		t.Errorf("Bootstrap returned %q, want empty string", got)
	}
}

// TestUIAPasswordCallbackUsesModernIdentifier pins the request shape we send to
// /keys/device_signing/upload's UIA password challenge. The mautrix helper
// emitted the legacy top-level "user" field, which Synapse forks reject with
// M_UNRECOGNIZED ("Identifier type not recognized"); we must use the modern
// "identifier" object instead.
func TestUIAPasswordCallbackUsesModernIdentifier(t *testing.T) {
	const (
		userID   = id.UserID("@bot:example.com")
		password = "hunter2"
		session  = "sess-1"
	)

	cb := uiaPasswordCallback(userID, password)
	if cb == nil {
		t.Fatal("uiaPasswordCallback returned nil")
	}

	body := cb(&mautrix.RespUserInteractive{Session: session})
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal callback body: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal callback body: %v", err)
	}

	want := map[string]interface{}{
		"type":     "m.login.password",
		"session":  session,
		"password": password,
		"identifier": map[string]interface{}{
			"type": "m.id.user",
			"user": string(userID),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("callback body =\n  %#v\nwant\n  %#v", got, want)
	}
}
