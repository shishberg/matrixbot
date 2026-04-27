package matrixbot

// Account holds the bot's long-lived secrets — the cross-signing recovery
// key (operator's only way back into the account if the server forgets
// the device) and the pickle key for the local crypto store. These
// survive logout: rotating an access token doesn't change the device
// identity, so wiping these on every logout would force the operator to
// re-verify with Element each time.
type Account struct {
	RecoveryKey string `json:"recovery_key"`
	PickleKey   string `json:"pickle_key"`
}

// LoadAccount reads account.json from dd. Missing file returns
// ErrNotInitialized.
func LoadAccount(dd DataDir) (Account, error) {
	var a Account
	if err := readJSON(dd.AccountPath(), &a); err != nil {
		return Account{}, err
	}
	return a, nil
}

// Save writes account.json to dd, creating the directory if needed.
func (a Account) Save(dd DataDir) error {
	if err := ensureDataDir(dd); err != nil {
		return err
	}
	return writeJSON(dd.AccountPath(), a)
}
