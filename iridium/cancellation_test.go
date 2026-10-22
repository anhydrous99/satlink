package iridium

import "testing"

func TestClonesShareOneRequest(t *testing.T) {
	// Copying the settings copies the reference to the one Cancellation.
	settings := DefaultDataCallSettings()
	clone := settings
	another := DefaultDataCallSettings()

	clone.Cancellation.Cancel()

	if !settings.Cancellation.Cancelled() {
		t.Error("cancelling a copy of the settings did not cancel the original")
	}
	if another.Cancellation.Cancelled() {
		t.Error("cancelling one call's settings cancelled another's")
	}
}

func TestAZeroCancellationIsReadyToUse(t *testing.T) {
	var cancellation Cancellation
	if cancellation.Cancelled() {
		t.Fatal("a zero Cancellation is cancelled before Cancel")
	}

	cancellation.Cancel()

	if !cancellation.Cancelled() {
		t.Error("a zero Cancellation is not cancelled after Cancel")
	}
}

func TestANilCancellationIsNeverCancelled(t *testing.T) {
	var cancellation *Cancellation

	if cancellation.Cancelled() {
		t.Error("a nil Cancellation is cancelled")
	}
}
