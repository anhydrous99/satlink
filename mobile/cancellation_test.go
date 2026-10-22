package mobile

import "testing"

func TestACancellationIsCancelledOnceCancelled(t *testing.T) {
	cases := map[string]*Cancellation{
		"one NewCancellation made":                        NewCancellation(),
		"a zero one, as gomobile's own constructor makes": {},
	}
	for name, cancellation := range cases {
		t.Run(name, func(t *testing.T) {
			before := cancellation.Cancelled()

			cancellation.Cancel()

			if before || !cancellation.Cancelled() {
				t.Errorf("cancelled before Cancel: %t, after: %t; want false, then true", before, cancellation.Cancelled())
			}
		})
	}
}

func TestTheLinksCallSettingsShareTheAppsCancellation(t *testing.T) {
	cancellation := NewCancellation()
	config, err := NewLinkSettings().config(cancellation)
	if err != nil {
		t.Fatalf("config(): %v", err)
	}

	cancellation.Cancel()

	if !config.Call.Cancellation.Cancelled() {
		t.Error("cancelling did not reach the link's call settings")
	}
}

func TestWithoutACancellationTheLinkHasOneNobodyCancels(t *testing.T) {
	config := linkConfig(t, NewLinkSettings())

	if config.Call.Cancellation == nil || config.Call.Cancellation.Cancelled() {
		t.Errorf("the link's own cancellation is %v, want one not cancelled", config.Call.Cancellation)
	}
}
