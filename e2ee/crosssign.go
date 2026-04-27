// Package e2ee is matrixbot's cross-signing and SAS-verification layer. It
// sits directly on top of mautrix, with no dependency on the rest of
// matrixbot: matrixbot's Bot.Run calls into it during steady-state startup,
// and a host program's Bootstrapper implementation calls Bootstrap during
// RunInit to mint the first-run cross-signing identity.
//
// Two pieces live here:
//   - Bootstrap (this file) generates or imports the cross-signing keys an
//     E2EE-aware Matrix client needs in order for Element to display
//     "Verify User" and to mark the bot's device as trusted.
//   - Verifier (verifier.go) drives an SAS (emoji) verification handshake
//     when the configured operator clicks "Verify Session" in Element.
package e2ee

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
)

// Bootstrap publishes or imports the bot's cross-signing keys.
//
// Cross-signing is a per-account identity that lets other clients verify the
// bot's individual devices without having to verify each one separately. The
// secret seeds are stashed in the homeserver's Secret Storage (SSSS),
// encrypted with a base58 "recovery key".
//
// Behaviour by argument:
//   - both empty: feature disabled, returns ("", nil) without touching mach.
//     Lets callers wire Bootstrap unconditionally for opt-out paths.
//   - recoveryKey set: import the existing seeds from SSSS and re-sign the
//     bot's own device. This is the steady-state path on every restart after
//     the first.
//   - password set (and recoveryKey empty): generate fresh cross-signing
//     keys, upload them, and stash the seeds in SSSS. The freshly minted
//     recovery key is returned for the caller to persist.
//
// recoveryKey wins when both are set — once you've bootstrapped you don't
// want to clobber the existing identity by accident.
//
// On the bootstrap path Bootstrap may return a non-empty recovery key
// alongside a non-nil error: mautrix generates the SSSS key first and only
// later does the UIA-gated upload, so a 401 leaves the caller with a
// half-bootstrapped account that can only be recovered with that key. The
// caller MUST persist the recovery key whenever it's non-empty, even when
// err is also non-nil.
func Bootstrap(ctx context.Context, mach *crypto.OlmMachine, password, recoveryKey string) (string, error) {
	if recoveryKey == "" && password == "" {
		return "", nil
	}
	if recoveryKey != "" {
		// Import the existing identity from SSSS; signing our own device marks
		// this session as trusted without changing the published public keys.
		if err := mach.VerifyWithRecoveryKey(ctx, recoveryKey); err != nil {
			return "", fmt.Errorf("importing cross-signing keys with recovery key: %w", err)
		}
		return "", nil
	}
	// First-run bootstrap. The empty passphrase makes mautrix generate a
	// random SSSS key; the returned string is its base58-encoded key
	// material, which the operator needs to restore cross-signing on
	// subsequent starts. The password gates the /keys/device_signing/upload
	// UIA challenge; if it's wrong the homeserver replies 401.
	rk, _, err := mach.GenerateAndUploadCrossSigningKeys(ctx, uiaPasswordCallback(mach.Client.UserID, password), "")
	if err != nil {
		return rk, fmt.Errorf("generating and uploading cross-signing keys: %w", err)
	}
	return rk, nil
}

// uiaPasswordCallback builds the UIA password-challenge response with the
// modern identifier object. mautrix's built-in helper sends the legacy
// top-level "user" field, which some homeservers reject as M_UNRECOGNIZED.
func uiaPasswordCallback(userID id.UserID, password string) mautrix.UIACallback {
	return func(uiResp *mautrix.RespUserInteractive) interface{} {
		return map[string]interface{}{
			"type":    string(mautrix.AuthTypePassword),
			"session": uiResp.Session,
			"identifier": map[string]interface{}{
				"type": "m.id.user",
				"user": string(userID),
			},
			"password": password,
		}
	}
}
