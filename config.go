package matrixbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrNotInitialized is returned when LoadConfig is called before
// init has written config.json. Callers check with errors.Is so they
// can produce a friendly "run init first" message.
var ErrNotInitialized = errors.New("data directory not initialized")

// Config is the operator-visible bot configuration. Everything in here is
// stable across logins — secrets (access token, recovery key, pickle key)
// live in Session and Account instead.
type Config struct {
	Homeserver     string                `json:"homeserver"`
	UserID         string                `json:"user_id"`
	OperatorUserID string                `json:"operator_user_id,omitempty"`
	AutoJoinRooms  []string              `json:"auto_join_rooms,omitempty"`
	Rooms          map[string]RoomConfig `json:"rooms,omitempty"`
}

// RoomConfig is one room's view: the set of routes that fire in this room,
// plus an opaque Extensions blob the host program decodes for handler
// credentials and per-room handler tuning. Routes within a room are
// evaluated in registration order; first match wins. An empty or missing
// routes list means the bot still joins the room but ignores every event
// it sees there.
type RoomConfig struct {
	// Extensions is host-decoded; matrixbot has no opinion on its shape.
	// One block per room is the source of truth for credentials shared by
	// every route in that room.
	Extensions json.RawMessage `json:"extensions,omitempty"`
	Routes     []RouteConfig   `json:"routes,omitempty"`
}

// RouteConfig binds one trigger kind to one handler kind. Trigger-shape
// fields (Prefix, Emoji) stay here because matrixbot itself reads them to
// decide whether the route's Trigger fires. Handler-side per-route knobs
// — page sizes, system prompts, anything else specific to one handler —
// live inside the opaque Extensions blob, which the host program decodes
// on its own terms. That mirrors RoomConfig.Extensions one level up:
// matrixbot has no opinion on its shape.
type RouteConfig struct {
	Trigger string `json:"trigger"`
	Handler string `json:"handler"`
	// Prefix is the leading token a "command" trigger looks for.
	Prefix string `json:"prefix,omitempty"`
	// Emoji is the unicode emoji a "reaction" trigger looks for.
	Emoji string `json:"emoji,omitempty"`
	// Extensions is host-decoded per-route configuration. matrixbot
	// preserves the bytes verbatim across save/load and never inspects
	// the contents.
	Extensions json.RawMessage `json:"extensions,omitempty"`
}

// LoadConfig reads config.json from dd. ErrNotInitialized wraps the
// underlying os.ErrNotExist so callers can detect the first-run case
// without string matching. A legacy single-room config (top-level
// `room_id` etc.) is rejected with an explicit "rerun init" message
// rather than silently migrated.
func LoadConfig(dd DataDir) (Config, error) {
	path := dd.ConfigPath()
	raw, err := readRawConfig(path)
	if err != nil {
		return Config{}, err
	}
	// Top-level keys from matrixbot's earlier single-room schema. Each one
	// here means the operator's config predates the per-room rooms map and
	// can't be silently migrated, because the new schema needs information
	// (which routes belong in which room) the old file doesn't carry.
	for _, k := range []string{"room_id", "target_room_id"} {
		if _, ok := raw[k]; ok {
			return Config{}, fmt.Errorf("config schema changed; please reinitialize this data directory and re-add your rooms by hand-editing %s", path)
		}
	}
	// A top-level "extensions" block was the v1 home for global handler
	// credentials. The current model puts one extensions block per room, so
	// a top-level block would be silently ignored — and the operator would
	// run with handlers that have no credentials. Surface it instead.
	if _, ok := raw["extensions"]; ok {
		return Config{}, fmt.Errorf("top-level \"extensions\" is no longer supported; move it under each room as rooms.<room_id>.extensions in %s", path)
	}
	if err := rejectLegacyRouteConfig(raw, path); err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(rawJSONBytes(raw), &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// rejectLegacyRouteConfig walks the rooms map looking for routes that
// still carry an inline "config" object. That field used to hold per-route
// handler credentials; those credentials now belong under
// rooms.<room_id>.extensions, so leaving them on the route silently
// disables them. We refuse rather than guess where to move the data.
func rejectLegacyRouteConfig(raw map[string]json.RawMessage, path string) error {
	roomsRaw, ok := raw["rooms"]
	if !ok {
		return nil
	}
	var rooms map[string]struct {
		Routes []map[string]json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(roomsRaw, &rooms); err != nil {
		// A malformed rooms map (not a JSON object, e.g.) is left for the
		// typed decode below to report — it produces a cleaner message
		// than we could here. Side-effect: if the operator's config has
		// both a malformed rooms map AND legacy per-route "config" fields,
		// only the malformed-rooms error fires; they'll see the legacy
		// migration error on their second load attempt, after fixing the
		// map. Acceptable because both are operator-side errors anyway.
		return nil
	}
	for roomID, room := range rooms {
		for i, route := range room.Routes {
			if _, ok := route["config"]; ok {
				return fmt.Errorf("route %s[%d] has a per-route \"config\" field; move handler credentials into rooms.%s.extensions in %s", roomID, i, roomID, path)
			}
		}
	}
	return nil
}

// readRawConfig reads config.json into a permissive map, preserving
// raw values for legacy-key detection without committing to the
// final struct shape.
func readRawConfig(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: missing %s", ErrNotInitialized, path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, nil
}

// rawJSONBytes re-marshals a permissive map back to bytes so a second
// pass can decode into the typed Config struct. Cheaper than reading
// the file twice.
func rawJSONBytes(m map[string]json.RawMessage) []byte {
	out, _ := json.Marshal(m)
	return out
}

// Save writes config.json to dd, creating the directory if needed.
func (c Config) Save(dd DataDir) error {
	if err := ensureDataDir(dd); err != nil {
		return err
	}
	return writeJSON(dd.ConfigPath(), c)
}

// ensureDataDir creates dd with mode 0700 if it doesn't exist. The data
// dir holds secrets, so we want it private to the operator from the
// moment it's created — never widen. The follow-up Chmod covers the
// pre-existing-directory case, where MkdirAll's mode is a no-op.
func ensureDataDir(dd DataDir) error {
	if err := os.MkdirAll(string(dd), 0o700); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dd, err)
	}
	if err := os.Chmod(string(dd), 0o700); err != nil {
		return fmt.Errorf("tightening data dir %s: %w", dd, err)
	}
	return nil
}

// writeJSON marshals v and atomically replaces path. The write goes to a
// sibling .tmp file first and is only renamed into place once the data
// is fully written; a crash mid-write leaves the prior file intact rather
// than truncating it, which matters because account.json holds the only
// copy of the recovery key.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// readJSON parses path into v. Missing file is mapped to ErrNotInitialized
// so callers can produce a single, consistent first-run error.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: missing %s", ErrNotInitialized, path)
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	return nil
}
