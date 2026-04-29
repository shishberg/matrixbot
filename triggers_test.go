package matrixbot

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestShouldHandleMentionAcceptsMention(t *testing.T) {
	got := shouldHandleMention(
		&event.MessageEventContent{Body: "hey @bot:example, please help"},
		id.UserID("@bot:example"),
		"@user:example",
	)
	if !got {
		t.Error("expected mention to be handled")
	}
}

func TestShouldHandleMentionRejectsOwnEcho(t *testing.T) {
	got := shouldHandleMention(
		&event.MessageEventContent{Body: "@bot:example self-talk"},
		id.UserID("@bot:example"),
		"@bot:example",
	)
	if got {
		t.Error("must not respond to own messages")
	}
}

func TestShouldHandleMentionRejectsUnmentioned(t *testing.T) {
	got := shouldHandleMention(
		&event.MessageEventContent{Body: "just a message"},
		id.UserID("@bot:example"),
		"@user:example",
	)
	if got {
		t.Error("must not respond when not mentioned")
	}
}

func TestShouldHandleMentionIgnoresLocalpartOnlyMention(t *testing.T) {
	// `@bot` (localpart alone) must NOT trigger the bot — that pattern
	// also matches strings like `@bot-admin` or quoted older messages,
	// which were causing false positives.
	got := shouldHandleMention(
		&event.MessageEventContent{Body: "hey @bot what's up"},
		id.UserID("@bot:example"),
		"@user:example",
	)
	if got {
		t.Error("localpart-only mention must not trigger the bot")
	}
}

func TestShouldHandleMentionUsesMentionsField(t *testing.T) {
	// The bare body doesn't contain the user ID, but the m.mentions field
	// lists it — that's how Element formats mentions in practice.
	got := shouldHandleMention(
		&event.MessageEventContent{
			Body:     "hey friend",
			Mentions: &event.Mentions{UserIDs: []id.UserID{"@bot:example"}},
		},
		id.UserID("@bot:example"),
		"@user:example",
	)
	if !got {
		t.Error("expected m.mentions-driven mention to be handled")
	}
}

func TestShouldHandleReactionAcceptsConfiguredEmoji(t *testing.T) {
	got, parent := shouldHandleReaction(
		&event.ReactionEventContent{RelatesTo: event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: id.EventID("$parent"),
			Key:     "📝",
		}},
		"@user:example",
		id.UserID("@bot:example"),
		"📝",
	)
	if !got {
		t.Error("expected pencil reaction to be handled")
	}
	if parent != id.EventID("$parent") {
		t.Errorf("parent = %q", parent)
	}
}

func TestShouldHandleReactionIgnoresOtherEmoji(t *testing.T) {
	got, _ := shouldHandleReaction(
		&event.ReactionEventContent{RelatesTo: event.RelatesTo{
			Type: event.RelAnnotation, EventID: id.EventID("$p"), Key: "👍",
		}},
		"@user:example",
		id.UserID("@bot:example"),
		"📝",
	)
	if got {
		t.Error("non-pencil reactions must be ignored")
	}
}

func TestShouldHandleReactionIgnoresOwnEcho(t *testing.T) {
	got, _ := shouldHandleReaction(
		&event.ReactionEventContent{RelatesTo: event.RelatesTo{
			Type: event.RelAnnotation, EventID: id.EventID("$p"), Key: "📝",
		}},
		"@bot:example",
		id.UserID("@bot:example"),
		"📝",
	)
	if got {
		t.Error("must not handle the bot's own reactions")
	}
}

func TestMentionTriggerApplyMatchesAndStripsMention(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{Body: "@bot:example what time is it?"}
	evt := &event.Event{
		ID:      id.EventID("$e1"),
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	req, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected match")
	}
	if req.Input != "what time is it?" {
		t.Errorf("Input = %q", req.Input)
	}
	if req.EventID != id.EventID("$e1") {
		t.Errorf("EventID = %q", req.EventID)
	}
	if req.ParentEventID != "" {
		t.Errorf("ParentEventID = %q, want empty", req.ParentEventID)
	}
}

func TestMentionTriggerApplyStructuredMentionKeepsLocalpartSubstring(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{
		Body:     "please ask @bot-admin to rotate the key",
		Mentions: &event.Mentions{UserIDs: []id.UserID{"@bot:example"}},
	}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	req, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected structured mention to match")
	}
	want := "please ask @bot-admin to rotate the key"
	if req.Input != want {
		t.Errorf("Input = %q, want %q", req.Input, want)
	}
}

func TestMentionTriggerApplyBodyMentionStripsFullMXID(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{Body: "@bot:example, run diagnostics"}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	req, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected body mention to match")
	}
	if req.Input != "run diagnostics" {
		t.Errorf("Input = %q", req.Input)
	}
}

func TestMentionTriggerApplyStructuredMentionStripsFullMXID(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{
		Body:     "@bot:example: check the audit log",
		Mentions: &event.Mentions{UserIDs: []id.UserID{"@bot:example"}},
	}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	req, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected structured mention to match")
	}
	if req.Input != "check the audit log" {
		t.Errorf("Input = %q", req.Input)
	}
}

func TestMentionTriggerApplySkipsEmptyAfterStripping(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{Body: "@bot:example"}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	_, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("a bare mention with no payload must not match")
	}
}

func TestMentionTriggerApplyStructuredMentionSkipsEmptyInput(t *testing.T) {
	mt := MentionTrigger{BotUserID: id.UserID("@bot:example")}
	mec := &event.MessageEventContent{
		Body:     "  :;,-  ",
		Mentions: &event.Mentions{UserIDs: []id.UserID{"@bot:example"}},
	}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:example"),
		Sender:  id.UserID("@user:example"),
		Content: event.Content{Parsed: mec},
	}
	_, ok, err := mt.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("structured mention with empty text must not match")
	}
}

func TestCommandTriggerMatchesPrefixAlone(t *testing.T) {
	ct := CommandTrigger{Prefix: "!tasks"}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:e"),
		Sender:  id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "!tasks"}},
	}
	req, ok, err := ct.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected match")
	}
	if req.Input != "" {
		t.Errorf("Input = %q, want empty", req.Input)
	}
}

func TestCommandTriggerMatchesPrefixWithArgs(t *testing.T) {
	ct := CommandTrigger{Prefix: "!tasks"}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:e"),
		Sender:  id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "!tasks  recent  "}},
	}
	req, ok, err := ct.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected match")
	}
	if req.Input != "recent" {
		t.Errorf("Input = %q", req.Input)
	}
	if req.ParentEventID != "" {
		t.Errorf("ParentEventID = %q, want empty", req.ParentEventID)
	}
}

func TestCommandTriggerRejectsPrefixSubstring(t *testing.T) {
	// "!tasksearch" must NOT match a "!tasks" prefix — that's the whole point
	// of the end-of-string-or-space rule.
	ct := CommandTrigger{Prefix: "!tasks"}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:e"),
		Sender:  id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "!tasksearch foo"}},
	}
	_, ok, err := ct.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("substring match must not fire")
	}
}

func TestCommandTriggerRejectsBotsOwnMessage(t *testing.T) {
	ct := CommandTrigger{
		Prefix:    "!tasks",
		BotUserID: id.UserID("@bot:e"),
	}
	evt := &event.Event{
		RoomID:  id.RoomID("!room:e"),
		Sender:  id.UserID("@bot:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "!tasks"}},
	}
	_, ok, _ := ct.Apply(context.Background(), evt, nil)
	if ok {
		t.Error("bot's own messages must not trigger commands")
	}
}

func TestReactionTriggerFetchesParentBody(t *testing.T) {
	parent := &event.Event{
		Type:    event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "the parent text"}},
	}
	fetcher := &fakeSender{parents: map[id.EventID]*event.Event{
		id.EventID("$parent"): parent,
	}}
	rt := ReactionTrigger{
		Emoji:     "📝",
		BotUserID: id.UserID("@bot:e"),
	}
	evt := &event.Event{
		RoomID: id.RoomID("!room:e"),
		Sender: id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{
				Type:    event.RelAnnotation,
				EventID: id.EventID("$parent"),
				Key:     "📝",
			},
		}},
	}
	req, ok, err := rt.Apply(context.Background(), evt, fetcher)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected match")
	}
	if req.Input != "the parent text" {
		t.Errorf("Input = %q", req.Input)
	}
	if req.ParentEventID != id.EventID("$parent") {
		t.Errorf("ParentEventID = %q, want %q", req.ParentEventID, id.EventID("$parent"))
	}
}

func TestReactionTriggerEmptyParentBodyDoesNotMatch(t *testing.T) {
	parent := &event.Event{
		Type:    event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{Body: ""}},
	}
	fetcher := &fakeSender{parents: map[id.EventID]*event.Event{
		id.EventID("$parent"): parent,
	}}
	rt := ReactionTrigger{
		Emoji:     "📝",
		BotUserID: id.UserID("@bot:e"),
	}
	evt := &event.Event{
		RoomID: id.RoomID("!room:e"),
		Sender: id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{
				Type: event.RelAnnotation, EventID: id.EventID("$parent"), Key: "📝",
			},
		}},
	}
	_, ok, err := rt.Apply(context.Background(), evt, fetcher)
	if err != nil {
		t.Errorf("Apply returned err = %v; an empty parent body should be (false, nil), not an error", err)
	}
	if ok {
		t.Error("empty parent body must not match")
	}
}

// TestReactionTriggerWithDecryptingFetcherReadsEncryptedParent pins the
// "callers don't care" guarantee: when the room is encrypted, ReactionTrigger
// fed through the decrypting fetcher matches and exposes the decrypted
// parent body.
func TestReactionTriggerWithDecryptingFetcherReadsEncryptedParent(t *testing.T) {
	encrypted := &event.Event{
		Type:   event.EventEncrypted,
		ID:     id.EventID("$parent"),
		RoomID: id.RoomID("!room:e"),
	}
	plaintext := &event.Event{
		Type:    event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "the parent text"}},
	}
	srv := newEventServer(t, encrypted)
	client := newClientForTest(t, srv.URL)
	client.Crypto = &fakeCryptoHelper{plaintext: plaintext}
	fetcher := newDecryptingFetcher(client)

	rt := ReactionTrigger{Emoji: "📝", BotUserID: id.UserID("@bot:e")}
	evt := &event.Event{
		RoomID: id.RoomID("!room:e"),
		Sender: id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{
				Type:    event.RelAnnotation,
				EventID: id.EventID("$parent"),
				Key:     "📝",
			},
		}},
	}
	req, ok, err := rt.Apply(context.Background(), evt, fetcher)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("expected match against encrypted parent decrypted via wrapper")
	}
	if req.Input != "the parent text" {
		t.Errorf("Input = %q, want %q", req.Input, "the parent text")
	}
	if req.ParentEventID != id.EventID("$parent") {
		t.Errorf("ParentEventID = %q, want %q", req.ParentEventID, id.EventID("$parent"))
	}
}

func TestReactionTriggerSurfacesFetcherError(t *testing.T) {
	fetcher := &fakeSender{getErr: errors.New("network down")}
	rt := ReactionTrigger{
		Emoji:     "📝",
		BotUserID: id.UserID("@bot:e"),
	}
	evt := &event.Event{
		RoomID: id.RoomID("!room:e"),
		Sender: id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{
				Type: event.RelAnnotation, EventID: id.EventID("$parent"), Key: "📝",
			},
		}},
	}
	_, ok, err := rt.Apply(context.Background(), evt, fetcher)
	if err == nil {
		t.Fatal("expected fetcher error to surface")
	}
	if ok {
		t.Error("ok must be false when fetcher errors")
	}
}
