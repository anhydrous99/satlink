package socket

import (
	"testing"
	"time"
)

func TestPollAtAccessors(t *testing.T) {
	t.Parallel()
	at := epoch.Add(3 * time.Second)
	cases := map[string]struct {
		poll          PollAt
		wantNow       bool
		wantTime      time.Time
		wantTimed     bool
		wantOnIngress bool
	}{
		"now":        {poll: PollNow(), wantNow: true},
		"at a time":  {poll: PollAtTime(at), wantTime: at, wantTimed: true},
		"on ingress": {poll: PollOnIngress(), wantOnIngress: true},
		"zero value": {poll: PollAt{}, wantNow: true},
	}
	for name, c := range cases {
		gotTime, gotTimed := c.poll.Time()
		if c.poll.IsNow() != c.wantNow || c.poll.IsOnIngress() != c.wantOnIngress ||
			gotTimed != c.wantTimed || !gotTime.Equal(c.wantTime) {
			t.Errorf("%s: IsNow %v, Time %v %v, IsOnIngress %v", name, c.poll.IsNow(), gotTime, gotTimed, c.poll.IsOnIngress())
		}
	}
}

func TestPollAtDeadline(t *testing.T) {
	t.Parallel()
	now := epoch.Add(time.Second)
	at := epoch.Add(3 * time.Second)
	cases := map[string]struct {
		poll         PollAt
		wantDeadline time.Time
		wantSet      bool
	}{
		"now is the current time": {poll: PollNow(), wantDeadline: now, wantSet: true},
		"a time is itself":        {poll: PollAtTime(at), wantDeadline: at, wantSet: true},
		"ingress sets none":       {poll: PollOnIngress(), wantSet: false},
	}
	for name, c := range cases {
		deadline, set := c.poll.Deadline(now)
		if set != c.wantSet || !deadline.Equal(c.wantDeadline) {
			t.Errorf("%s: Deadline = %v, %v; want %v, %v", name, deadline, set, c.wantDeadline, c.wantSet)
		}
	}
}

func TestEarliestPrefersNowThenTheEarliestTime(t *testing.T) {
	t.Parallel()
	early := PollAtTime(epoch.Add(time.Second))
	late := PollAtTime(epoch.Add(2 * time.Second))
	cases := map[string]struct {
		polls []PollAt
		want  PollAt
	}{
		"nothing waits":          {polls: nil, want: PollOnIngress()},
		"only ingress":           {polls: []PollAt{PollOnIngress(), PollOnIngress()}, want: PollOnIngress()},
		"a time beats ingress":   {polls: []PollAt{PollOnIngress(), late}, want: late},
		"the earlier time wins":  {polls: []PollAt{late, early, PollOnIngress()}, want: early},
		"now beats every time":   {polls: []PollAt{early, PollNow(), late}, want: PollNow()},
		"now beats ingress":      {polls: []PollAt{PollOnIngress(), PollNow()}, want: PollNow()},
		"equal times keep first": {polls: []PollAt{early, early}, want: early},
	}
	for name, c := range cases {
		if got := Earliest(c.polls...); !got.Equal(c.want) {
			t.Errorf("%s: Earliest = %+v, want %+v", name, got, c.want)
		}
	}
}

func TestPollAtEqualComparesInstants(t *testing.T) {
	t.Parallel()
	instant := epoch.Add(time.Second)
	sameInstantElsewhere := instant.In(time.FixedZone("elsewhere", 3600))

	if !PollAtTime(instant).Equal(PollAtTime(sameInstantElsewhere)) {
		t.Error("PollAt values of the same instant in two locations are not Equal")
	}
	if PollAtTime(instant).Equal(PollNow()) || PollNow().Equal(PollOnIngress()) {
		t.Error("PollAt values of different kinds are Equal")
	}
}

func TestPollAtStringNamesTheVariant(t *testing.T) {
	t.Parallel()
	cases := []struct {
		poll PollAt
		want string
	}{
		{PollNow(), "Now"},
		{PollAtTime(epoch.Add(1500 * time.Millisecond)), "Time(0001-01-01 00:00:01.5 +0000 UTC)"},
		{PollOnIngress(), "Ingress"},
	}
	for _, c := range cases {
		if got := c.poll.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}
