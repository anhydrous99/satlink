package satnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// unnoticedDeadline is a context whose deadline has passed though its Err
// is still nil, as a real context's is for a moment after its deadline.
type unnoticedDeadline struct {
	context.Context
	deadline time.Time
}

func (c unnoticedDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestDialContextReportsTheContextOnlyOnceItsDeadlineHasPassed(t *testing.T) {
	lookupTimedOut := &Error{Kind: ErrTimedOut, Operation: operationDNSLookup}
	refused := &Error{Kind: ErrConnectionFailed}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	distant, stop := context.WithTimeout(context.Background(), time.Hour)
	defer stop()
	passed := unnoticedDeadline{Context: context.Background(), deadline: time.Now()}
	for _, c := range []struct {
		name  string
		ctx   context.Context
		err   error
		cause error
	}{
		{"a timeout at the context's deadline", passed, lookupTimedOut, context.DeadlineExceeded},
		{"a cancelled context", cancelled, refused, context.Canceled},
		{"a timeout long before the deadline", distant, lookupTimedOut, nil},
		{"a timeout without a deadline", context.Background(), lookupTimedOut, nil},
		{"another failure at the deadline", passed, refused, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := dialError(c.ctx, "tcp", c.err)

			var opErr *net.OpError
			switch {
			case c.cause == nil && err != c.err:
				t.Errorf("dialError = %v, want the dial's own error %v", err, c.err)
			case c.cause != nil && (!errors.As(err, &opErr) || opErr.Op != "dial" || !errors.Is(err, c.cause)):
				t.Errorf("dialError = %v, want a dial *net.OpError for %v", err, c.cause)
			}
		})
	}
}

func TestNothingNewStartsWhileTheLinkDrains(t *testing.T) {
	sharedEngine := upEngine(t)
	sharedEngine.update(func(e *engine) { e.beginDraining() })
	link := &Link{shared: sharedEngine, policy: policyOf(t, "echo.test:7", echoEndpoint.String())}
	for _, c := range []struct {
		name  string
		start func() error
	}{
		{"a lookup", func() error {
			_, err := link.Resolve("echo.test", waitBudget)
			return err
		}},
		{"a TCP connection", func() error {
			_, err := link.Dial(echoEndpoint.Addr().String(), echoEndpoint.Port(), waitBudget)
			return err
		}},
		{"a UDP socket", func() error {
			_, err := link.DialUDP(echoEndpoint.Addr().String(), echoEndpoint.Port(), waitBudget)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.start()

			// The close delivers only what was written before it.
			var linkErr *Error
			if !errors.As(err, &linkErr) || linkErr.Kind != ErrLinkDown || linkErr.Reason != (CloseReason{Kind: CloseReasonRequested}) {
				t.Errorf("%s while the link drains = %v, want ErrLinkDown for the requested close", c.name, err)
			}
		})
	}
}

// multiHost is a name the dial tests' link knows two addresses for, and
// multiHostPort the port it is dialled on.
const (
	multiHost     = "multi.test"
	multiHostPort = 7
)

func TestATimeoutOrAnEndedContextEndsADialsAttemptsAtOnce(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name     string
		ctx      context.Context
		deadline time.Time
		want     error
	}{
		{"a timeout", context.Background(), time.Now(), ErrTimedOut},
		{"the dial's context ending", cancelled, time.Time{}, context.Canceled},
	} {
		t.Run(c.name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			sharedEngine.update(func(e *engine) {
				e.names.remember(multiHost, []netip.Addr{ipv4(192, 0, 2, 50), ipv4(192, 0, 2, 51)}, time.Now())
			})
			link := &Link{shared: sharedEngine, policy: policyOf(t, multiHost+":*")}

			// No driver runs the stack: no connection is ever answered.
			_, err := link.dial(c.ctx, multiHost, multiHostPort, c.deadline)

			if !errors.Is(err, c.want) {
				t.Errorf("dial = %v, want %v", err, c.want)
			}
			// Every attempt that failed was let go of.
			e := sharedEngine.lock()
			defer sharedEngine.unlock()
			if attempts := len(e.stack.closingTCP); attempts != 1 {
				t.Errorf("%d addresses tried, want the first alone", attempts)
			}
		})
	}
}
