package mobile

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTheListenerStopsOnceItHasDeliveredTheClose(t *testing.T) {
	t.Parallel()
	link := simulatedLink(t)
	listener := &StateRecorder{}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		deliverStateChanges(link.watcher, listener)
	}()

	if _, err := link.HangUp(linkTimeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(ListenerPatience):
		t.Fatalf("the listener still follows the link %v after it closed", ListenerPatience)
	}
	AssertHeardEveryChange(t, listener)
}

func TestAListenerThatCannotFollowItsLinkLogsItsPanic(t *testing.T) {
	// No t.Parallel: recordErrors replaces the process-wide logger.
	records := recordErrors(t)

	// A watcher of no link stands in for a fault in following the link,
	// which the recovery around the app's OnStateChanged does not cover.
	deliverStateChanges(nil, &StateRecorder{})

	logged := records()
	if !slices.ContainsFunc(logged, func(text string) bool { return strings.HasPrefix(text, "the link listener panicked: ") }) {
		t.Errorf("logged %q, want the link listener's panic", logged)
	}
}
