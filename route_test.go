package matrixbot

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestTriggerFuncAdapter(t *testing.T) {
	want := Request{Input: "hello", RoomID: id.RoomID("!r:e")}
	called := false
	tf := TriggerFunc(func(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error) {
		called = true
		return want, true, nil
	})
	got, ok, err := tf.Apply(context.Background(), &event.Event{}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !called {
		t.Fatal("underlying func was not called")
	}
	if !ok {
		t.Error("ok = false")
	}
	if got.Input != want.Input || got.RoomID != want.RoomID {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestHandlerFuncAdapter(t *testing.T) {
	called := false
	hf := HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
		called = true
		if req.Input != "in" {
			return Response{}, errors.New("bad input")
		}
		return Response{Reply: "out"}, nil
	})
	resp, err := hf.Handle(context.Background(), Request{Input: "in"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Fatal("underlying func was not called")
	}
	if resp.Reply != "out" {
		t.Errorf("reply = %q", resp.Reply)
	}
}
