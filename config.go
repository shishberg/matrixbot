package matrixbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/robfig/cron/v3"
	"maunium.net/go/mautrix/id"
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
	// Cron is the standard 5-field cron expression a "schedule" trigger
	// fires on (minute hour day-of-month month day-of-week).
	Cron string `json:"cron,omitempty"`
	// Input is the synthetic Request.Input a "schedule" trigger hands the
	// handler when it fires. Other triggers ignore this field.
	Input string `json:"input,omitempty"`
	// Extensions is host-decoded per-route configuration. matrixbot
	// preserves the bytes verbatim across save/load and never inspects
	// the contents.
	Extensions json.RawMessage `json:"extensions,omitempty"`
}

// BuildTrigger turns this route's trigger fields into one of matrixbot's
// built-in Trigger implementations.
func (r RouteConfig) BuildTrigger(botUserID id.UserID) (Trigger, error) {
	switch r.Trigger {
	case "mention":
		return MentionTrigger{BotUserID: botUserID}, nil
	case "command":
		if r.Prefix == "" {
			return nil, fmt.Errorf("command trigger requires a non-empty prefix")
		}
		return CommandTrigger{Prefix: r.Prefix, BotUserID: botUserID}, nil
	case "reaction":
		if r.Emoji == "" {
			return nil, fmt.Errorf("reaction trigger requires a non-empty emoji")
		}
		return ReactionTrigger{Emoji: r.Emoji, BotUserID: botUserID}, nil
	case "schedule":
		if r.Cron == "" {
			return nil, fmt.Errorf("schedule trigger requires a non-empty cron expression")
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(r.Cron)
		if err != nil {
			return nil, fmt.Errorf("schedule trigger cron %q: %w", r.Cron, err)
		}
		return &ScheduleTrigger{Schedule: sched, CronExpr: r.Cron, Input: r.Input}, nil
	default:
		return nil, fmt.Errorf("unknown trigger kind %q", r.Trigger)
	}
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
	if err := validateConfigSchema(raw, path); err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(rawJSONBytes(raw), &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

func validateConfigSchema(raw map[string]json.RawMessage, path string) error {
	for _, key := range []string{"room_id", "target_room_id", "extensions"} {
		if _, ok := raw[key]; ok {
			return validateTopLevelConfigKey(key, path)
		}
	}
	for key := range raw {
		if err := validateTopLevelConfigKey(key, path); err != nil {
			return err
		}
	}
	roomsRaw, ok := raw["rooms"]
	if !ok {
		return nil
	}
	var rooms map[string]map[string]json.RawMessage
	if err := json.Unmarshal(roomsRaw, &rooms); err != nil {
		// A malformed rooms map (not a JSON object, e.g.) is left for the
		// typed decode below to report — it produces a cleaner message
		// than a schema preflight can.
		return nil
	}
	for roomID, roomRaw := range rooms {
		for key := range roomRaw {
			if !isAllowedRoomConfigKey(key) {
				return fmt.Errorf("unknown config field rooms[%q].%s in %s", roomID, key, path)
			}
		}
		routesRaw, ok := roomRaw["routes"]
		if !ok {
			continue
		}
		var routes []map[string]json.RawMessage
		if err := json.Unmarshal(routesRaw, &routes); err != nil {
			return nil
		}
		for i, route := range routes {
			if _, ok := route["config"]; ok {
				return fmt.Errorf("route rooms[%q].routes[%d] has a per-route \"config\" field; move handler credentials into rooms[%q].extensions in %s", roomID, i, roomID, path)
			}
			if _, ok := route["limit"]; ok {
				return fmt.Errorf("route rooms[%q].routes[%d] has a legacy \"limit\" field; move it to rooms[%q].routes[%d].extensions.limit in %s", roomID, i, roomID, i, path)
			}
			for key := range route {
				if !isAllowedRouteConfigKey(key) {
					return fmt.Errorf("unknown config field rooms[%q].routes[%d].%s in %s", roomID, i, key, path)
				}
			}
		}
	}
	return nil
}

func validateTopLevelConfigKey(key, path string) error {
	switch key {
	case "homeserver", "user_id", "operator_user_id", "auto_join_rooms", "rooms":
		return nil
	case "room_id", "target_room_id":
		return fmt.Errorf("config schema changed; please reinitialize this data directory and re-add your rooms by hand-editing %s", path)
	case "extensions":
		return fmt.Errorf("top-level \"extensions\" is no longer supported; move it under each room as rooms.<room_id>.extensions in %s", path)
	default:
		return fmt.Errorf("unknown config field %q in %s", key, path)
	}
}

func isAllowedRoomConfigKey(key string) bool {
	switch key {
	case "extensions", "routes":
		return true
	default:
		return false
	}
}

func isAllowedRouteConfigKey(key string) bool {
	switch key {
	case "trigger", "handler", "prefix", "emoji", "cron", "input", "extensions":
		return true
	default:
		return false
	}
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

func ensureSecretsDir(dd DataDir) error {
	if err := ensureDataDir(dd); err != nil {
		return err
	}
	return ensurePrivateDir(dd.SecretsDir())
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("creating secrets dir %s: %w", path, err)
	}
	for _, dir := range privateDirsToTighten(path) {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("tightening secrets dir %s: %w", dir, err)
		}
	}
	return nil
}

func privateDirsToTighten(path string) []string {
	clean := filepath.Clean(path)
	dirs := []string{clean}
	for dir := clean; filepath.Base(dir) != ".secrets"; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs[:1]
		}
		dir = parent
		dirs = append(dirs, dir)
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// WriteSecret writes bytes to a secret path with private file and directory
// modes. The caller chooses the path with DataDir.SecretPath or
// DataDir.ExtensionSecretPath.
func WriteSecret(path string, data []byte) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale temp file %s: %w", tmp, err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("setting mode on %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s -> %s: %w", tmp, path, err)
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
	return WriteSecret(path, data)
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
