package matrixbot

// Session is the rotatable login state. The access token is invalidated
// by the server on logout (or on any other client's "remove device"
// action), so it lives separately from Account (which holds the bot's
// long-lived cross-signing identity).
type Session struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
}

// LoadSession reads session.json from dd. Missing file returns
// ErrNotInitialized.
func LoadSession(dd DataDir) (Session, error) {
	if err := migrateLegacySecrets(dd); err != nil {
		return Session{}, err
	}
	var s Session
	if err := readJSON(dd.SessionPath(), &s); err != nil {
		return Session{}, err
	}
	return s, nil
}

// Save writes session.json to dd, creating the directory if needed.
func (s Session) Save(dd DataDir) error {
	if err := ensureSecretsDir(dd); err != nil {
		return err
	}
	return writeJSON(dd.SessionPath(), s)
}
