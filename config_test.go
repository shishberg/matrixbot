package matrixbot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	in := Config{
		Homeserver:     "https://matrix.example",
		UserID:         "@bot:example",
		OperatorUserID: "@dave:example",
		AutoJoinRooms:  []string{"!a:example", "!b:example"},
		Rooms: map[string]RoomConfig{
			"!a:example": {
				Extensions: json.RawMessage(`{"mopoke":{"base_url":"https://m","token":"t","workspace":"eng"}}`),
				Routes: []RouteConfig{
					{Trigger: "mention", Handler: "llm"},
					{Trigger: "command", Prefix: "!tasks", Handler: "mopoke_list", Limit: 20},
				},
			},
		},
	}
	if err := in.Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadConfig(dd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Homeserver != in.Homeserver || got.UserID != in.UserID || got.OperatorUserID != in.OperatorUserID {
		t.Errorf("scalar fields lost: got %+v, want %+v", got, in)
	}
	if len(got.AutoJoinRooms) != 2 || got.AutoJoinRooms[0] != "!a:example" || got.AutoJoinRooms[1] != "!b:example" {
		t.Errorf("AutoJoinRooms = %v", got.AutoJoinRooms)
	}
	if len(got.Rooms) != 1 || len(got.Rooms["!a:example"].Routes) != 2 {
		t.Errorf("Rooms = %+v", got.Rooms)
	}
	if r := got.Rooms["!a:example"].Routes[1]; r.Prefix != "!tasks" || r.Handler != "mopoke_list" || r.Limit != 20 {
		t.Errorf("route round-trip lost data: %+v", r)
	}
	var ext map[string]map[string]string
	if err := json.Unmarshal(got.Rooms["!a:example"].Extensions, &ext); err != nil {
		t.Fatalf("unmarshal room extensions: %v", err)
	}
	if ext["mopoke"]["token"] != "t" {
		t.Errorf("room extensions round-trip lost data: %v", ext)
	}
}

func TestConfigSaveMode0600(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := (Config{Homeserver: "h", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dd.ConfigPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestLoadConfigMissingReturnsErrNotInitialized(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(DataDir(dir))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err = %v, want ErrNotInitialized", err)
	}
}

func TestConfigSaveLeavesNoTempFile(t *testing.T) {
	// Save uses write-temp-then-rename for atomicity. The temp file MUST
	// be cleaned up so we don't accumulate stale .tmp files (and so the
	// data dir doesn't leak partial JSON to anyone reading it).
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := (Config{Homeserver: "h", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(string(dd))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestConfigSaveOverwritesAtomicallyAfterPriorWrite(t *testing.T) {
	// Verify the second Save fully replaces the first — a temp+rename
	// implementation must succeed even when the destination already
	// exists (rename(2) on POSIX overwrites).
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := (Config{Homeserver: "h1", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := (Config{Homeserver: "h2", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err := LoadConfig(dd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Homeserver != "h2" {
		t.Errorf("Homeserver = %q, want second value", got.Homeserver)
	}
}

func TestLoadConfigRejectsLegacyRoomIDField(t *testing.T) {
	// A pre-multi-room config.json with a top-level "room_id" must NOT be
	// silently migrated; it must produce an actionable error so the
	// operator knows to rerun init and rebuild the rooms map by hand.
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := []byte(`{"homeserver":"h","user_id":"u","room_id":"!old:e"}`)
	if err := os.WriteFile(dd.ConfigPath(), legacy, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(dd)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "schema changed") {
		t.Errorf("err should mention 'schema changed', got %q", err)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("err should suggest rerunning init, got %q", err)
	}
}

func TestConfigRoomExtensionsRoundTrip(t *testing.T) {
	// Room.Extensions is an opaque blob; matrixbot must preserve byte-for-byte
	// what the host wrote, and the on-disk JSON key must be the neutral
	// "extensions" nested under the room rather than any host-specific term.
	dir := t.TempDir()
	dd := DataDir(dir)
	in := Config{
		Homeserver: "h", UserID: "u",
		Rooms: map[string]RoomConfig{
			"!a:e": {
				Extensions: json.RawMessage(`{"alpha":{"k":"v"}}`),
				Routes:     []RouteConfig{{Trigger: "mention", Handler: "llm"}},
			},
		},
	}
	if err := in.Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(dd.ConfigPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `"extensions"`) {
		t.Errorf("on-disk JSON missing \"extensions\" key: %s", raw)
	}
	got, err := LoadConfig(dd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	var roundTripped map[string]map[string]string
	if err := json.Unmarshal(got.Rooms["!a:e"].Extensions, &roundTripped); err != nil {
		t.Fatalf("unmarshal Extensions: %v", err)
	}
	if roundTripped["alpha"]["k"] != "v" {
		t.Errorf("Extensions round-trip lost data: %v", roundTripped)
	}
}

func TestLoadConfigRejectsTopLevelExtensions(t *testing.T) {
	// In the per-room model, a top-level "extensions" block is meaningless —
	// every credential set lives under a specific room. Surfacing it as a
	// hard error stops the operator from quietly running with a config that
	// looks fine but never feeds any handler.
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := []byte(`{"homeserver":"h","user_id":"u","extensions":{"mopoke":{"base_url":"x"}}}`)
	if err := os.WriteFile(dd.ConfigPath(), legacy, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(dd)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "extensions") {
		t.Errorf("err should name extensions, got %q", err)
	}
	if !strings.Contains(err.Error(), "room") {
		t.Errorf("err should suggest moving under each room, got %q", err)
	}
}

func TestLoadConfigRejectsLegacyPerRouteConfig(t *testing.T) {
	// A pre-per-room config blob lived inline on each route under "config".
	// The operator now declares one extensions block per room and routes
	// just name handlers + per-route knobs (limit/prefix/emoji), so leaving
	// "config" on a route is almost certainly leftover credentials we'd
	// ignore. Refuse and tell them where to move it.
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := []byte(`{"homeserver":"h","user_id":"u","rooms":{"!a:e":{"routes":[{"trigger":"mention","handler":"llm","config":{"foo":"bar"}}]}}}`)
	if err := os.WriteFile(dd.ConfigPath(), legacy, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(dd)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("err should name the config field, got %q", err)
	}
	if !strings.Contains(err.Error(), "extensions") {
		t.Errorf("err should point at extensions as the new home, got %q", err)
	}
}

func TestRouteConfigOmitsLegacyConfigField(t *testing.T) {
	// RouteConfig must not serialise a per-route "config" key any more.
	// The legacy detector above relies on that key being absent in
	// fresh writes, so a future re-add of the field would silently
	// re-trigger the legacy error on every save round-trip.
	r := RouteConfig{Trigger: "mention", Handler: "llm"}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"config"`) {
		t.Errorf("RouteConfig still serialises \"config\": %s", out)
	}
}

func TestConfigSaveCreatesDataDirWith0700(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(filepath.Join(dir, "fresh"))
	if err := (Config{Homeserver: "h", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(string(dd))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 0700", got)
	}
}
