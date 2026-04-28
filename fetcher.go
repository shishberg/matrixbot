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
	decrypted, err := f.client.Crypto.Decrypt(ctx, evt)
	if err != nil {
		return nil, err
	}
	decrypted.Mautrix.EventSource |= event.SourceDecrypted
	return decrypted, nil
}
