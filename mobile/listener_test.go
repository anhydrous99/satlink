package mobile

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtest"
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
	// No t.Parallel: logtest.Record replaces the process-wide logger.
	records := logtest.Record(t, logPackage, slog.LevelError)

	// A watcher of no link stands in for a fault in following the link,
	// which the recovery around the app's OnStateChanged does not cover.
	deliverStateChanges(nil, &StateRecorder{})

	logged := records()
	if !slices.ContainsFunc(logged, func(line logtest.Line) bool { return strings.HasPrefix(line.Text, "the link listener panicked: ") }) {
		t.Errorf("logged %+v, want the link listener's panic", logged)
	}
}
