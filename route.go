package matrixbot

import (
	"context"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Request is the trigger-extracted view of a Matrix event passed to a Handler.
// Input is whatever payload the trigger pulled out — for a mention it's the
// text after stripping `@bot`; for a reaction it's the parent message body.
type Request struct {
	EventID id.EventID
	RoomID  id.RoomID
	Sender  id.UserID
	Input   string
}

// Response is what a Handler returns. An empty Reply means "stay quiet".
type Response struct {
	Reply string
}

// EventFetcher is the subset of *mautrix.Client that triggers need to resolve
// referenced events (e.g. fetching the parent of a reaction). Both
// *mautrix.Client and the test fakeSender satisfy it.
type EventFetcher interface {
	GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*event.Event, error)
}

// Trigger decides whether a route fires for a given event and, if so,
// extracts a Request for the handler. A non-match is (_, false, nil); a real
// failure (e.g. fetcher error) is (_, false, err).
type Trigger interface {
	Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error)
}

// Handler processes a matched Request and returns a Response.
type Handler interface {
	Handle(ctx context.Context, req Request) (Response, error)
}

type TriggerFunc func(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error)

func (f TriggerFunc) Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error) {
	return f(ctx, evt, fetcher)
}

type HandlerFunc func(ctx context.Context, req Request) (Response, error)

func (f HandlerFunc) Handle(ctx context.Context, req Request) (Response, error) {
	return f(ctx, req)
}
