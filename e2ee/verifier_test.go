package e2ee

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestNewVerifierEmptyOperatorReturnsNil pins the "feature disabled"
// contract: empty operator user ID returns nil so the caller can short-circuit.
func TestNewVerifierEmptyOperatorReturnsNil(t *testing.T) {
	if v := NewVerifier(nil, ""); v != nil {
		t.Errorf("NewVerifier(\"\") = %v, want nil", v)
	}
}

// fakeDriver records every call the Verifier makes so tests can assert both
// that the right method fired and that the wrong one didn't.
type fakeDriver struct {
	acceptCalls  []id.VerificationTransactionID
	dismissCalls []id.VerificationTransactionID
	confirmCalls []id.VerificationTransactionID
	cancelCalls  []cancelCall
}

type cancelCall struct {
	txnID  id.VerificationTransactionID
	code   event.VerificationCancelCode
	reason string
}

func (f *fakeDriver) AcceptVerification(_ context.Context, txnID id.VerificationTransactionID) error {
	f.acceptCalls = append(f.acceptCalls, txnID)
	return nil
}

func (f *fakeDriver) DismissVerification(_ context.Context, txnID id.VerificationTransactionID) error {
	f.dismissCalls = append(f.dismissCalls, txnID)
	return nil
}

func (f *fakeDriver) CancelVerification(_ context.Context, txnID id.VerificationTransactionID, code event.VerificationCancelCode, reason string) error {
	f.cancelCalls = append(f.cancelCalls, cancelCall{txnID, code, reason})
	return nil
}

func (f *fakeDriver) ConfirmSAS(_ context.Context, txnID id.VerificationTransactionID) error {
	f.confirmCalls = append(f.confirmCalls, txnID)
	return nil
}

const (
	testOperator    id.UserID                    = "@operator:example.com"
	testNonOperator id.UserID                    = "@stranger:example.com"
	testTxnID       id.VerificationTransactionID = "txn-1"
	testDeviceID    id.DeviceID                  = "DEV1"
)

func newTestVerifier(driver verificationDriver) *Verifier {
	return &Verifier{
		operatorID: testOperator,
		helper:     driver,
	}
}

// TestVerificationRequestedAcceptsOperator pins the operator gate's positive
// case: a request whose `from` matches operatorID must be accepted, and must
// not also be dismissed.
func TestVerificationRequestedAcceptsOperator(t *testing.T) {
	driver := &fakeDriver{}
	v := newTestVerifier(driver)

	v.VerificationRequested(context.Background(), testTxnID, testOperator, testDeviceID)

	if got := len(driver.acceptCalls); got != 1 || driver.acceptCalls[0] != testTxnID {
		t.Errorf("acceptCalls = %v, want [%s]", driver.acceptCalls, testTxnID)
	}
	if got := len(driver.dismissCalls); got != 0 {
		t.Errorf("dismissCalls = %v, want none", driver.dismissCalls)
	}
}

// TestVerificationRequestedDismissesNonOperator pins the operator gate's
// negative case: a request from anyone other than operatorID must be
// dismissed and never accepted, otherwise a stranger could mint a verified
// channel by spamming requests.
func TestVerificationRequestedDismissesNonOperator(t *testing.T) {
	driver := &fakeDriver{}
	v := newTestVerifier(driver)

	v.VerificationRequested(context.Background(), testTxnID, testNonOperator, testDeviceID)

	if got := len(driver.dismissCalls); got != 1 || driver.dismissCalls[0] != testTxnID {
		t.Errorf("dismissCalls = %v, want [%s]", driver.dismissCalls, testTxnID)
	}
	if got := len(driver.acceptCalls); got != 0 {
		t.Errorf("acceptCalls = %v, want none", driver.acceptCalls)
	}
}

// TestVerificationReadyWaitsForPeerStart pins the bug fix: as the responder
// we must NOT send m.key.verification.start ourselves. The operator's client
// always initiates start when the user clicks "Verify with emoji"; if we also
// called StartSAS, both sides set StartedByUs=true, neither sends accept, and
// the protocol deadlocks.
func TestVerificationReadyWaitsForPeerStart(t *testing.T) {
	driver := &fakeDriver{}
	v := newTestVerifier(driver)

	v.VerificationReady(context.Background(), testTxnID, testDeviceID, true, false, nil)

	if got := len(driver.cancelCalls); got != 0 {
		t.Errorf("cancelCalls = %v, want none", driver.cancelCalls)
	}
	if got := len(driver.acceptCalls); got != 0 {
		t.Errorf("acceptCalls = %v, want none", driver.acceptCalls)
	}
	if got := len(driver.dismissCalls); got != 0 {
		t.Errorf("dismissCalls = %v, want none", driver.dismissCalls)
	}
	if got := len(driver.confirmCalls); got != 0 {
		t.Errorf("confirmCalls = %v, want none", driver.confirmCalls)
	}
}

// TestVerificationReadyCancelsWhenSASUnsupported pins the fallback: a peer
// without SAS gets cancelled with the standard "unknown method" code rather
// than left dangling.
func TestVerificationReadyCancelsWhenSASUnsupported(t *testing.T) {
	driver := &fakeDriver{}
	v := newTestVerifier(driver)

	v.VerificationReady(context.Background(), testTxnID, testDeviceID, false, false, nil)

	if got := len(driver.cancelCalls); got != 1 {
		t.Fatalf("cancelCalls = %v, want one entry", driver.cancelCalls)
	}
	got := driver.cancelCalls[0]
	if got.txnID != testTxnID {
		t.Errorf("cancel txnID = %s, want %s", got.txnID, testTxnID)
	}
	if got.code != event.VerificationCancelCodeUnknownMethod {
		t.Errorf("cancel code = %s, want %s", got.code, event.VerificationCancelCodeUnknownMethod)
	}
}

// TestShowSASConfirms pins the auto-confirm step: once mautrix has surfaced
// the SAS, we relay confirmation for the same transaction.
func TestShowSASConfirms(t *testing.T) {
	driver := &fakeDriver{}
	v := newTestVerifier(driver)

	v.ShowSAS(context.Background(), testTxnID, []rune{'a'}, []string{"alpha"}, []int{1, 2, 3})

	if got := len(driver.confirmCalls); got != 1 || driver.confirmCalls[0] != testTxnID {
		t.Errorf("confirmCalls = %v, want [%s]", driver.confirmCalls, testTxnID)
	}
}
