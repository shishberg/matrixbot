package matrixbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// fakeCryptoHelper records the event handed to Decrypt and returns a
// pre-canned plaintext. It's only enough to satisfy mautrix.CryptoHelper for
// the Decrypt path the wrapper exercises; everything else returns zero
// values so we can avoid pulling in olm/CGO from tests.
type fakeCryptoHelper struct {
	called    int
	got       *event.Event
	plaintext *event.Event
	err       error
}

var _ mautrix.CryptoHelper = (*fakeCryptoHelper)(nil)

func (f *fakeCryptoHelper) Encrypt(ctx context.Context, _ id.RoomID, _ event.Type, _ any) (*event.EncryptedEventContent, error) {
	return nil, nil
}

func (f *fakeCryptoHelper) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	f.called++
	f.got = evt
	if f.err != nil {
		return nil, f.err
	}
	return f.plaintext, nil
}

func (f *fakeCryptoHelper) WaitForSession(_ context.Context, _ id.RoomID, _ id.SenderKey, _ id.SessionID, _ time.Duration) bool {
	return true
}

func (f *fakeCryptoHelper) RequestSession(_ context.Context, _ id.RoomID, _ id.SenderKey, _ id.SessionID, _ id.UserID, _ id.DeviceID) {
}

func (f *fakeCryptoHelper) Init(_ context.Context) error { return nil }

// newEventServer stands up an httptest server that replies to every
// /rooms/.../event/... GET with the supplied event JSON. mautrix.NewClient
// pointed at server.URL routes GetEvent through it.
func newEventServer(t *testing.T, evt *event.Event) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newClientForTest(t *testing.T, serverURL string) *mautrix.Client {
	t.Helper()
	client, err := mautrix.NewClient(serverURL, "@bot:e", "tok")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// TestDecryptingFetcherDecryptsEncryptedEvent pins the contract: when the
// server returns m.room.encrypted and the client has crypto enabled, the
// wrapper hands the event to Crypto.Decrypt and returns the decrypted
// result.
func TestDecryptingFetcherDecryptsEncryptedEvent(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$enc"),
		RoomID: id.RoomID("!r:e"),
	}
	plaintext := &event.Event{
		Type:    event.EventMessage,
		ID:      id.EventID("$enc"),
		RoomID:  id.RoomID("!r:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "decrypted body"}},
	}
	srv := newEventServer(t, encrypted)
	client := newClientForTest(t, srv.URL)
	helper := &fakeCryptoHelper{plaintext: plaintext}
	client.Crypto = helper

	f := newDecryptingFetcher(client)
	got, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$enc"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if helper.called != 1 {
		t.Errorf("Decrypt called %d times, want 1", helper.called)
	}
	if got == nil || parentEventBody(got) != "decrypted body" {
		t.Errorf("got body = %q, want %q", parentEventBody(got), "decrypted body")
	}
}

// TestDecryptingFetcherSetsDecryptedSourceBit pins symmetry with
// cryptohelper.postDecrypt, which OR's event.SourceDecrypted into
// Mautrix.EventSource after a successful decrypt. Callers that inspect
// EventSource must see the same state regardless of whether the event
// arrived via the syncer or via the wrapper.
func TestDecryptingFetcherSetsDecryptedSourceBit(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$enc"),
		RoomID: id.RoomID("!r:e"),
	}
	plaintext := &event.Event{
		Type:    event.EventMessage,
		ID:      id.EventID("$enc"),
		RoomID:  id.RoomID("!r:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "decrypted body"}},
	}
	srv := newEventServer(t, encrypted)
	client := newClientForTest(t, srv.URL)
	client.Crypto = &fakeCryptoHelper{plaintext: plaintext}

	f := newDecryptingFetcher(client)
	got, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$enc"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Mautrix.EventSource&event.SourceDecrypted == 0 {
		t.Errorf("EventSource = %b, want SourceDecrypted bit set", got.Mautrix.EventSource)
	}
}

// TestDecryptingFetcherSkipsDecryptForPlaintext pins the no-op path: a
// plain m.room.message event must be returned as-is without consulting
// Decrypt — both for performance and because Decrypt errors on non-encrypted
// inputs.
func TestDecryptingFetcherSkipsDecryptForPlaintext(t *testing.T) {
	plain := &event.Event{
		Type:    event.EventMessage,
		ID:      id.EventID("$plain"),
		RoomID:  id.RoomID("!r:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "plain body"}},
	}
	srv := newEventServer(t, plain)
	client := newClientForTest(t, srv.URL)
	helper := &fakeCryptoHelper{}
	client.Crypto = helper

	f := newDecryptingFetcher(client)
	got, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$plain"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if helper.called != 0 {
		t.Errorf("Decrypt called %d times, want 0 for plaintext", helper.called)
	}
	if got == nil || got.Type != event.EventMessage {
		t.Errorf("got = %+v", got)
	}
}

// TestDecryptingFetcherEncryptedWithoutCryptoReturnsRaw pins the no-crypto
// degradation: if the host runs without a pickle key, client.Crypto is nil
// and the wrapper must return the encrypted event unchanged rather than
// panic. That preserves today's behaviour for unencrypted-only deployments.
func TestDecryptingFetcherEncryptedWithoutCryptoReturnsRaw(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$enc"),
		RoomID: id.RoomID("!r:e"),
	}
	srv := newEventServer(t, encrypted)
	client := newClientForTest(t, srv.URL)
	// client.Crypto deliberately left nil.

	f := newDecryptingFetcher(client)
	got, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$enc"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got == nil || got.Type != event.EventEncrypted {
		t.Errorf("got = %+v, want encrypted event passed through", got)
	}
}

// TestDecryptingFetcherSurfacesDecryptError pins that decrypt failures
// propagate. Silently falling back to the encrypted event would just
// reproduce the silent-no-match bug at a different layer.
func TestDecryptingFetcherSurfacesDecryptError(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$enc"),
		RoomID: id.RoomID("!r:e"),
	}
	srv := newEventServer(t, encrypted)
	client := newClientForTest(t, srv.URL)
	wantErr := errors.New("no session")
	client.Crypto = &fakeCryptoHelper{err: wantErr}

	f := newDecryptingFetcher(client)
	_, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$enc"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// TestDecryptingFetcherNilEventReturnsNilNil pins the defensive guard:
// if the server responds with `null` JSON (mautrix decodes that to a nil
// *event.Event with no error), the wrapper must short-circuit and not
// hand a nil pointer to Crypto.Decrypt.
func TestDecryptingFetcherNilEventReturnsNilNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := newClientForTest(t, srv.URL)
	helper := &fakeCryptoHelper{}
	client.Crypto = helper

	f := newDecryptingFetcher(client)
	got, err := f.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$nope"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
	if helper.called != 0 {
		t.Errorf("Decrypt called %d times, want 0 (nil event must short-circuit)", helper.called)
	}
}

// TestNewBotFetcherDecryptsViaClientCrypto pins the wiring: NewBot must
// install the decrypting wrapper, and a Crypto set on the client AFTER
// NewBot (as Run does) must still be consulted on subsequent fetches.
func TestNewBotFetcherDecryptsViaClientCrypto(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$enc"),
		RoomID: id.RoomID("!r:e"),
	}
	plaintext := &event.Event{
		Type:    event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "decrypted body"}},
	}
	srv := newEventServer(t, encrypted)

	bot, err := NewBot(BotConfig{
		Homeserver:  srv.URL,
		UserID:      id.UserID("@bot:e"),
		AccessToken: "tok",
		DeviceID:    id.DeviceID("DEV"),
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	helper := &fakeCryptoHelper{plaintext: plaintext}
	bot.client.Crypto = helper

	got, err := bot.fetcher.GetEvent(context.Background(), id.RoomID("!r:e"), id.EventID("$enc"))
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if helper.called != 1 {
		t.Errorf("Decrypt called %d times, want 1 (NewBot must install the decrypting wrapper)", helper.called)
	}
	if got == nil || parentEventBody(got) != "decrypted body" {
		t.Errorf("got body = %q, want %q", parentEventBody(got), "decrypted body")
	}
}
