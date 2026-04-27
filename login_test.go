package matrixbot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

func TestRunLoginPersistsServerReturnedDeviceID(t *testing.T) {
	dir := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "https://matrix.example", UserID: "@bot:example"}).Save(dir); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	login := &fakeInitLoginClient{
		resp: &mautrix.RespLogin{AccessToken: "syt_new", DeviceID: id.DeviceID("RETURNED")},
	}
	prompter := &cannedPrompter{answers: map[string]string{"bot password": "hunter2"}}
	out := &bytes.Buffer{}
	deps := LoginDeps{
		LoginFactory: func(homeserver string) (LoginClient, error) {
			if homeserver != "https://matrix.example" {
				t.Errorf("homeserver = %q", homeserver)
			}
			return login, nil
		},
		Prompter: prompter,
		Stdout:   out,
	}
	if err := RunLogin(context.Background(), dir, deps); err != nil {
		t.Fatalf("RunLogin: %v", err)
	}

	sess, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if sess.AccessToken != "syt_new" {
		t.Errorf("AccessToken = %q", sess.AccessToken)
	}
	if sess.DeviceID != "RETURNED" {
		t.Errorf("DeviceID = %q", sess.DeviceID)
	}
	if !strings.Contains(out.String(), "RETURNED") {
		t.Errorf("stdout should report device id, got %q", out.String())
	}
	if strings.Contains(out.String(), "syt_new") {
		t.Errorf("stdout MUST NOT contain access token, got %q", out.String())
	}
}

func TestRunLoginErrorsWhenServerOmitsDeviceID(t *testing.T) {
	// An empty device_id in the response would clobber the on-disk
	// device id and silently shed the cross-signing link on next start.
	// Refuse early instead.
	dir := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "h", UserID: "@bot:e"}).Save(dir); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	login := &fakeInitLoginClient{
		resp: &mautrix.RespLogin{AccessToken: "syt_new", DeviceID: ""},
	}
	prompter := &cannedPrompter{answers: map[string]string{"bot password": "hunter2"}}
	deps := LoginDeps{
		LoginFactory: func(string) (LoginClient, error) { return login, nil },
		Prompter:     prompter,
		Stdout:       &bytes.Buffer{},
	}
	err := RunLogin(context.Background(), dir, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "device_id") {
		t.Errorf("err should mention device_id, got %q", err)
	}
	if _, statErr := os.Stat(dir.SessionPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("session.json must not be written when device_id is missing")
	}
}

func TestRunLoginErrorsWhenConfigMissing(t *testing.T) {
	dir := DataDir(t.TempDir() + "/.matrixbot")
	deps := LoginDeps{
		LoginFactory: func(string) (LoginClient, error) {
			t.Fatal("LoginFactory should not be called")
			return nil, nil
		},
		Prompter: &cannedPrompter{},
		Stdout:   &bytes.Buffer{},
	}
	err := RunLogin(context.Background(), dir, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("err should suggest running init, got %q", err)
	}
}

func TestRunLoginAlwaysPromptsForPasswordEvenIfEnvHasOne(t *testing.T) {
	// login exists for token rotation; reading MATRIX_PASSWORD
	// from env at runtime would defeat the whole point of moving secrets
	// out of env. The user MUST type the password.
	dir := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "h", UserID: "@bot:e"}).Save(dir); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("MATRIX_PASSWORD", "should-be-ignored")
	login := &fakeInitLoginClient{
		resp: &mautrix.RespLogin{AccessToken: "tok", DeviceID: id.DeviceID("D")},
	}
	prompter := &cannedPrompter{answers: map[string]string{"bot password": "typed"}}
	deps := LoginDeps{
		LoginFactory: func(string) (LoginClient, error) { return login, nil },
		Prompter:     prompter,
		Stdout:       &bytes.Buffer{},
	}
	if err := RunLogin(context.Background(), dir, deps); err != nil {
		t.Fatalf("RunLogin: %v", err)
	}
	if login.gotReq.Password != "typed" {
		t.Errorf("login used password %q, want %q", login.gotReq.Password, "typed")
	}
	if len(prompter.calls) == 0 {
		t.Error("prompter must be called for password")
	}
}

func TestRunLoginScrubsPasswordFromError(t *testing.T) {
	dir := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "h", UserID: "@bot:e"}).Save(dir); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	login := &fakeInitLoginClient{err: errors.New("400: server echoed hunter2 in body")}
	prompter := &cannedPrompter{answers: map[string]string{"bot password": "hunter2"}}
	deps := LoginDeps{
		LoginFactory: func(string) (LoginClient, error) { return login, nil },
		Prompter:     prompter,
		Stdout:       &bytes.Buffer{},
	}
	err := RunLogin(context.Background(), dir, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks password: %q", err)
	}
}
