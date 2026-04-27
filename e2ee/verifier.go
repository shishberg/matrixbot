package e2ee

import (
	"context"
	"fmt"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Verifier handles incoming SAS (emoji) verification requests for the bot's
// device.
//
// The flow we implement (driven by mautrix's verificationhelper):
//  1. Operator clicks "Verify Session" in Element. mautrix surfaces it as a
//     VerificationRequested callback.
//  2. We auto-accept iff the request comes from the configured operator user
//     ID. Anyone else gets dismissed — the bot is a single-user tool and we
//     don't want a stranger able to mint a "verified" device by spamming
//     requests.
//  3. VerificationReady fires once both sides have agreed. The bot is always
//     the responder, so we deliberately do nothing here: the operator's
//     client sends m.key.verification.start when they click "Verify with
//     emoji", and mautrix's helper auto-sends the matching accept (it only
//     does so when StartedByUs is false). If we also called StartSAS, both
//     sides would have StartedByUs=true and neither would send accept,
//     deadlocking the exchange.
//  4. ShowSAS fires with the emoji/decimal short authentication string.
//     We log it prominently so the operator can compare it against Element.
//  5. We auto-confirm with ConfirmSAS. The operator's "they don't match" tap
//     in Element cancels the transaction before our confirm matters; their
//     "they match" tap implies they trust the bot. A more conservative
//     design would relay the operator's decision through a chat message,
//     but that requires a second secure channel and isn't worth the
//     complexity for a personal bot.
//  6. VerificationDone / VerificationCancelled wrap things up; we just log.
type Verifier struct {
	client     *mautrix.Client
	operatorID id.UserID
	helper     verificationDriver
}

// verificationDriver is the subset of *verificationhelper.VerificationHelper
// that Verifier actually drives. It exists so tests can substitute a fake and
// pin the operator gate's behaviour without standing up a homeserver.
type verificationDriver interface {
	AcceptVerification(ctx context.Context, txnID id.VerificationTransactionID) error
	DismissVerification(ctx context.Context, txnID id.VerificationTransactionID) error
	CancelVerification(ctx context.Context, txnID id.VerificationTransactionID, code event.VerificationCancelCode, reason string) error
	ConfirmSAS(ctx context.Context, txnID id.VerificationTransactionID) error
}

// NewVerifier constructs a Verifier that only trusts requests from
// operatorUserID. If operatorUserID is empty, verification is disabled and
// nil is returned — callers should check for nil before calling Init.
//
// The returned value is not yet active; call Init once the OlmMachine is
// ready (i.e. after cryptohelper.Init has succeeded).
func NewVerifier(client *mautrix.Client, operatorUserID id.UserID) *Verifier {
	if operatorUserID == "" {
		return nil
	}
	return &Verifier{
		client:     client,
		operatorID: operatorUserID,
	}
}

// Init wires the verification helper into the client's syncer. After Init
// returns, inbound verification events will trigger the callback methods on v.
//
// The in-memory verification store is fine here: SAS verifications complete
// in a single connected sync session. A persistent store would only matter if
// we needed to resume an interrupted handshake across restarts, and Element
// just retries when that happens.
func (v *Verifier) Init(ctx context.Context, mach *crypto.OlmMachine) error {
	// NewVerificationHelper panics if client.Crypto is nil. Convert that to a
	// regular error so the caller gets a clean failure if Init is invoked
	// before cryptohelper.Init has wired the client.
	if v.client.Crypto == nil {
		return fmt.Errorf("client.Crypto not set: call cryptohelper.Init before verifier.Init")
	}
	// SAS is the only method an operator on a phone can complete without us
	// building a QR-display UI.
	helper := verificationhelper.NewVerificationHelper(v.client, mach, nil, v, false, false, true)
	if err := helper.Init(ctx); err != nil {
		return fmt.Errorf("initialising verification helper: %w", err)
	}
	v.helper = helper
	slog.Info("e2ee: verifier ready", "operator", v.operatorID)
	return nil
}

// VerificationRequested fires when a remote device asks to start a
// verification with us. We accept only requests from the configured
// operator; everything else is dismissed so an attacker can't gain a
// verified channel by retrying.
func (v *Verifier) VerificationRequested(ctx context.Context, txnID id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	if from != v.operatorID {
		slog.Warn("e2ee: verification request from non-operator, dismissing", "from", from, "device", fromDevice, "txn", txnID)
		if err := v.helper.DismissVerification(ctx, txnID); err != nil {
			slog.Warn("e2ee: dismiss verification failed", "txn", txnID, "err", err)
		}
		return
	}
	slog.Info("e2ee: accepting verification request from operator", "from", from, "device", fromDevice, "txn", txnID)
	if err := v.helper.AcceptVerification(ctx, txnID); err != nil {
		slog.Warn("e2ee: accept verification failed", "txn", txnID, "err", err)
	}
}

// VerificationReady fires once both sides have agreed to verify. We then
// wait for the operator's client to send m.key.verification.start. Calling
// StartSAS here would deadlock the protocol: both sides would set
// StartedByUs=true, so neither would send m.key.verification.accept.
func (v *Verifier) VerificationReady(ctx context.Context, txnID id.VerificationTransactionID, otherDeviceID id.DeviceID, supportsSAS, supportsScanQRCode bool, qrCode *verificationhelper.QRCode) {
	slog.Info("e2ee: verification ready", "txn", txnID, "other_device", otherDeviceID, "sas", supportsSAS)
	if !supportsSAS {
		slog.Warn("e2ee: peer does not support SAS, cancelling", "txn", txnID)
		if err := v.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeUnknownMethod, "only SAS is supported"); err != nil {
			slog.Warn("e2ee: cancel verification failed", "txn", txnID, "err", err)
		}
	}
}

// ShowSAS fires when mautrix has computed the short authentication string.
// Log it so the operator can compare it against what Element shows, then
// immediately confirm. This is safe because a "Don't match" tap in Element
// sends a cancel event, which mautrix processes before the MAC exchange
// completes, voiding the verification.
func (v *Verifier) ShowSAS(ctx context.Context, txnID id.VerificationTransactionID, emojis []rune, emojiDescriptions []string, decimals []int) {
	slog.Info("e2ee: SAS", "txn", txnID, "emojis", string(emojis), "descriptions", emojiDescriptions, "decimals", decimals)
	if err := v.helper.ConfirmSAS(ctx, txnID); err != nil {
		slog.Warn("e2ee: confirm SAS failed", "txn", txnID, "err", err)
	}
}

// VerificationCancelled is logged with the reason so the operator can see
// why a verification didn't complete (typically: timed out, mismatch, or
// the other side bailed).
func (v *Verifier) VerificationCancelled(ctx context.Context, txnID id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	slog.Info("e2ee: verification cancelled", "txn", txnID, "code", code, "reason", reason)
}

// VerificationDone fires once both sides have exchanged MACs and the
// verification is durable. The bot's device is now signed by the operator's
// cross-signing identity.
func (v *Verifier) VerificationDone(ctx context.Context, txnID id.VerificationTransactionID, method event.VerificationMethod) {
	slog.Info("e2ee: verification done", "txn", txnID, "method", method)
}
