package matrixbot

import (
	"context"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// decryptingFetcher lets EventFetcher callers ignore encryption.
// *mautrix.Client.GetEvent is a plain HTTP fetch and returns m.room.encrypted
// envelopes verbatim; the syncer routes encrypted sync events through
// cryptohelper.HandleEncrypted, but one-off fetches don't. Wrapping the
// client here makes the two paths symmetric.
type decryptingFetcher struct {
	client *mautrix.Client
}

func newDecryptingFetcher(client *mautrix.Client) *decryptingFetcher {
	return &decryptingFetcher{client: client}
}

func (f *decryptingFetcher) GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	evt, err := f.client.GetEvent(ctx, roomID, eventID)
	if err != nil {
		return nil, err
	}
	if f.client.Crypto == nil || evt == nil || evt.Type != event.EventEncrypted {
		return evt, nil
	}
	// GetEvent leaves Content.Parsed nil — only the syncer's per-type
	// dispatch parses content before handing it to Decrypt, and Decrypt
	// asserts the parsed type. Parse here so the GET path matches.
	// ParseRaw refuses to clobber an already-parsed value, so guard the
	// nil case explicitly (test fakes can pre-populate Parsed).
	if evt.Content.Parsed == nil {
		if err := evt.Content.ParseRaw(evt.Type); err != nil {
			return nil, err
		}
	}
	decrypted, err := f.client.Crypto.Decrypt(ctx, evt)
	if err != nil {
		return nil, err
	}
	decrypted.Mautrix.EventSource |= event.SourceDecrypted
	return decrypted, nil
}
