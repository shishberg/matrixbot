package matrixbot

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// shouldHandleMention returns true when the message is one we should reply
// to: not from the bot itself, mentioning the bot by full user ID in the body
// or via m.mentions, and leaving non-empty input. Room scoping is the
// dispatcher's responsibility, not the trigger's.
func shouldHandleMention(content *event.MessageEventContent, botUserID id.UserID, sender string) bool {
	_, ok := mentionInput(content, botUserID, sender)
	return ok
}

func mentionInput(content *event.MessageEventContent, botUserID id.UserID, sender string) (string, bool) {
	if content == nil {
		return "", false
	}
	if sender == string(botUserID) {
		return "", false
	}

	body := content.Body
	if strings.Contains(body, string(botUserID)) {
		return nonEmptyMentionInput(stripFullUserID(body, botUserID))
	}
	if content.Mentions != nil && content.Mentions.Has(botUserID) {
		return nonEmptyMentionInput(body)
	}
	return "", false
}

func stripFullUserID(body string, botUserID id.UserID) string {
	needle := string(botUserID)
	if needle == "" {
		return body
	}
	if idx := strings.Index(body, needle); idx >= 0 {
		return body[:idx] + body[idx+len(needle):]
	}
	return body
}

func nonEmptyMentionInput(body string) (string, bool) {
	input := trimMentionInput(body)
	if input == "" {
		return "", false
	}
	return input, true
}

func trimMentionInput(body string) string {
	body = strings.TrimSpace(body)
	body = strings.TrimLeft(body, ":,;- ")
	return strings.TrimSpace(body)
}

// shouldHandleReaction returns whether a reaction event should fire, plus
// the parent message's event ID. Triggers only when the emoji matches and
// the sender isn't the bot itself (so the bot's own reactions don't loop).
// Room scoping is the dispatcher's responsibility.
func shouldHandleReaction(content *event.ReactionEventContent, sender string, botUserID id.UserID, emoji string) (bool, id.EventID) {
	if content == nil {
		return false, ""
	}
	if sender == string(botUserID) {
		return false, ""
	}
	if content.RelatesTo.Key != emoji {
		return false, ""
	}
	if content.RelatesTo.EventID == "" {
		return false, ""
	}
	return true, content.RelatesTo.EventID
}

// parentEventBody pulls the plain-text body out of a fetched message event,
// returning "" when the parent isn't a text message we can use.
func parentEventBody(evt *event.Event) string {
	if evt == nil {
		return ""
	}
	// Content.Parsed isn't guaranteed populated on a freshly-fetched event;
	// fall back to parsing the raw content.
	if mec, ok := evt.Content.Parsed.(*event.MessageEventContent); ok && mec != nil {
		return mec.Body
	}
	if err := evt.Content.ParseRaw(event.EventMessage); err == nil {
		if mec, ok := evt.Content.Parsed.(*event.MessageEventContent); ok && mec != nil {
			return mec.Body
		}
	}
	return ""
}

// MentionTrigger fires when a message mentions BotUserID and has
// non-empty text after stripping the mention. The dispatcher restricts
// which rooms the trigger sees.
type MentionTrigger struct {
	BotUserID id.UserID
}

func (m MentionTrigger) Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error) {
	mec, _ := evt.Content.Parsed.(*event.MessageEventContent)
	text, ok := mentionInput(mec, m.BotUserID, string(evt.Sender))
	if !ok {
		return Request{}, false, nil
	}
	return Request{
		EventID: evt.ID,
		RoomID:  evt.RoomID,
		Sender:  evt.Sender,
		Input:   text,
	}, true, nil
}

// CommandTrigger fires when a message body starts with Prefix followed by
// either end-of-string or whitespace. Input is whatever follows the prefix,
// trimmed. Match is case-sensitive. The dispatcher restricts which rooms
// the trigger sees.
type CommandTrigger struct {
	Prefix    string
	BotUserID id.UserID
}

func (c CommandTrigger) Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error) {
	if c.BotUserID != "" && evt.Sender == c.BotUserID {
		return Request{}, false, nil
	}
	mec, _ := evt.Content.Parsed.(*event.MessageEventContent)
	if mec == nil {
		return Request{}, false, nil
	}
	body := strings.TrimSpace(mec.Body)
	if !strings.HasPrefix(body, c.Prefix) {
		return Request{}, false, nil
	}
	rest := body[len(c.Prefix):]
	// The prefix must be followed by end-of-string or whitespace, otherwise
	// "!tasksearch" would match a "!tasks" prefix.
	if rest != "" && !isASCIISpace(rest[0]) {
		return Request{}, false, nil
	}
	return Request{
		EventID: evt.ID,
		RoomID:  evt.RoomID,
		Sender:  evt.Sender,
		Input:   strings.TrimSpace(rest),
	}, true, nil
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// ReactionTrigger fires when a reaction with Emoji is added by someone
// other than the bot. It uses the EventFetcher to retrieve the parent
// message body, which becomes Input. The dispatcher restricts which rooms
// the trigger sees.
type ReactionTrigger struct {
	Emoji     string
	BotUserID id.UserID
}

func (r ReactionTrigger) Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error) {
	rec, _ := evt.Content.Parsed.(*event.ReactionEventContent)
	ok, parentID := shouldHandleReaction(rec, string(evt.Sender), r.BotUserID, r.Emoji)
	if !ok {
		return Request{}, false, nil
	}
	if fetcher == nil {
		return Request{}, false, nil
	}
	parent, err := fetcher.GetEvent(ctx, evt.RoomID, parentID)
	if err != nil {
		return Request{}, false, err
	}
	body := parentEventBody(parent)
	if body == "" {
		return Request{}, false, nil
	}
	return Request{
		EventID:       evt.ID,
		ParentEventID: parentID,
		RoomID:        evt.RoomID,
		Sender:        evt.Sender,
		Input:         body,
	}, true, nil
}
