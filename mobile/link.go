package mobile

import (
	"maps"
	"slices"
	"sync"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/satnet"
)

// The app-private satellite link (package satnet). Timeouts are plain
// milliseconds rather than durations: Kotlin's java.time.Duration needs
// Android API 26, and a negative Swift TimeInterval would crash at the
// boundary. They are int64, as are the settings' seconds, so that a "wait
// forever" such as Long.MAX_VALUE arrives intact (see "Integers" in the
// package documentation).

// The stages of [LinkState].Kind.
const (
	// LinkStateNegotiating means the call is up and PPP is negotiating.
	LinkStateNegotiating = iota
	// LinkStateUp means IP traffic flows.
	LinkStateUp
	// LinkStateClosing means the link is ending: after a requested close,
	// data already sent is still delivered first; then the call is hung up.
	LinkStateClosing
	// LinkStateClosed means the link is gone and the call has ended —
	// unless LinkStatus.CallMayStillBeUp says the hang-up failed.
	LinkStateClosed
)

// The reasons of [LinkCloseReason].Kind.
const (
	// LinkCloseReasonRequested means the app hung up.
	LinkCloseReasonRequested = iota
	// LinkCloseReasonCarrierLost means the call dropped: signal lost, or the
	// network ended it. Redial if needed.
	LinkCloseReasonCarrierLost
	// LinkCloseReasonIdleTimeout means there was no application traffic for
	// the idle timeout.
	LinkCloseReasonIdleTimeout
	// LinkCloseReasonCallTimeLimit means the maximum call duration was
	// reached.
	LinkCloseReasonCallTimeLimit
	// LinkCloseReasonDataLimit means the data volume limit was reached.
	LinkCloseReasonDataLimit
	// LinkCloseReasonNegotiationTimedOut means PPP did not come up in time.
	LinkCloseReasonNegotiationTimedOut
	// LinkCloseReasonPPP means PPP went down: peer terminated,
	// authentication failed, … (Detail).
	LinkCloseReasonPPP
	// LinkCloseReasonTransport means talking to the handset failed
	// (Detail).
	LinkCloseReasonTransport
	// LinkCloseReasonInternal means a fault inside the library (Detail). The
	// link still hangs up, and LinkStatus.CallMayStillBeUp says if that
	// failed.
	LinkCloseReasonInternal
)

// LinkSettings are what the link may reach and spend. Create them with
// NewLinkSettings, which gomobile makes the constructor. Limits too small to
// be of use are refused with SatlinkErrorInvalidArgument before anything is
// dialled: a call time under 24 seconds (bringing PPP up and hanging up),
// an idle time under 25 seconds (a DNS lookup may wait 10 seconds for each
// of two servers), or a data limit under 2048 bytes (PPP alone takes up to
// 500).
//
// A call that is handed the settings — EstablishLink,
// EstablishLinkWithHandset or EstablishSerialLink — reads them once, as it
// starts: changing them afterwards changes nothing for its link. Allow and
// AddHost may be called from any thread meanwhile; the fields, though, must
// not be set while another thread hands the settings to a call.
type LinkSettings struct {
	// Number is dialled for the data call.
	Number string
	// Username is the PPP user name; Iridium's gateway accepts anything.
	Username string
	// Password is the PPP password; Iridium's gateway accepts anything.
	Password string
	// MaxCallSeconds is the longest the call may last, hanging up
	// included: the hang-up starts early enough to end the call in time; 0
	// for no limit.
	MaxCallSeconds int64
	// IdleTimeoutSeconds hangs up after this many seconds without
	// application traffic; 0 for no limit.
	IdleTimeoutSeconds int64
	// MaxBytes hangs up once this many bytes crossed the link; 0 for no
	// limit.
	MaxBytes int64
	// CloseGraceSeconds is how long, when closing, data already sent may
	// take to arrive before the call is hung up.
	CloseGraceSeconds int64

	// mu guards allowRules and hosts: the app may add to them from one
	// thread while another hands the settings to a call, which copies them.
	mu         sync.Mutex
	allowRules []string
	hosts      map[string]string
}

// NewLinkSettings returns settings for Iridium's internet gateway with the
// library's limits — a 60-minute call limit, a 5-minute idle timeout, no
// data limit, a 30-second close grace — that allow no destination yet.
// Negative limits, and those too small to be of use (see LinkSettings), are
// refused when the link is established, before dialling.
func NewLinkSettings() *LinkSettings {
	defaults := satnet.NewConfig(satnet.DenyAll())
	return &LinkSettings{
		Number:             defaults.Number,
		Username:           defaults.Credentials.Username,
		Password:           defaults.Credentials.Password,
		MaxCallSeconds:     wholeSeconds(defaults.Limits.MaxCallDuration),
		IdleTimeoutSeconds: wholeSeconds(defaults.Limits.IdleTimeout),
		MaxBytes:           countFromUnsigned(defaults.Limits.MaxBytes),
		CloseGraceSeconds:  wholeSeconds(defaults.Tuning.CloseGrace),
		hosts:              map[string]string{},
	}
}

// Allow also allows rule, a destination such as "relay.example.com:443" or
// "203.0.113.0/24:8000-8099". Nothing else is ever contacted. Rules are
// checked when the link is established.
func (s *LinkSettings) Allow(rule string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowRules = append(s.allowRules, rule)
}

// AddHost answers name locally with the IPv4 address ipv4Address, saving
// DNS airtime. The address is checked when the link is established; a
// name added twice keeps the last address.
func (s *LinkSettings) AddHost(name, ipv4Address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts == nil {
		s.hosts = map[string]string{}
	}
	s.hosts[name] = ipv4Address
}

// entries returns copies of the rules and hosts added so far, which no
// other thread changes.
func (s *LinkSettings) entries() (allowRules []string, hosts map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.allowRules), maps.Clone(s.hosts)
}

// LinkCloseReason is why a link ended.
type LinkCloseReason struct {
	// Kind is one of the LinkCloseReason constants.
	Kind int
	// Detail says what failed (PPP, Transport, Internal).
	Detail string
}

// LinkState is where a link is in its life.
type LinkState struct {
	// Kind is one of the LinkState constants.
	Kind int
	// Reason is why the link is closing or closed, and nil before: the only
	// optional field of this package's results.
	Reason *LinkCloseReason
}

// LinkStatus reports a link at one moment. Its State and DNSServers are
// never nil.
type LinkStatus struct {
	// State is where the link is in its life.
	State *LinkState
	// ConnectMessage is what the handset reported after CONNECT, usually
	// the data rate.
	ConnectMessage string
	// LocalAddress is our IPv4 address on the link; "" until PPP is up.
	LocalAddress string
	// DNSServers are the DNS servers PPP was offered; an empty list until
	// PPP is up.
	DNSServers *StringList
	// ConnectedSeconds is the billed airtime so far, the hang-up included
	// once it is over.
	ConnectedSeconds int64
	// BytesSent counts the bytes written to the handset, framing included.
	BytesSent int64
	// BytesReceived counts the bytes read from the handset, framing
	// included.
	BytesReceived int64
	// CallMayStillBeUp reports that the hang-up could not be confirmed
	// (neither ATH after the escape nor a DTR drop was), so the call may
	// still be up and billing. IridiumHandset.Reinitialize tries to end it;
	// power-cycling the handset is the last resort.
	CallMayStillBeUp bool
}

// LinkListener is told about every change of a link's state, on a thread of
// its own, until the link has closed. An error it returns is ignored: the
// notifications go on. Hold the link weakly in the listener, or the two
// keep each other alive.
//
// In Objective-C and Swift, adopt the protocol SatlinkMobileLinkListener
// (Swift: SatlinkMobileLinkListenerProtocol) in a class of your own, as
// for ByteTransport: the class of that name is not for apps to make.
type LinkListener interface {
	// OnStateChanged is told the link's new state, never nil.
	OnStateChanged(state *LinkState) error
}

// SatelliteLink is an IP link over an Iridium data call that only this app
// can use. It is safe for use from several threads.
//
// Hanging up goes through the transport, so keep the transport open until
// the call has ended: call HangUp, in a finally block, before closing it. A
// link merely dropped as its transport closes may leave the call up.
type SatelliteLink struct {
	// link is the link itself; nil in a zero value.
	link *satnet.Link
	// watcher follows the link's state, for Status and HangUp and for the
	// listener.
	watcher *satnet.Watcher
}

// EstablishLink dials through a transport the app provides (on Android, the
// USB connection), initialises the handset and brings the link up. It
// blocks for the call setup, about half a minute over Iridium, unless
// cancellation, if not nil, stops it from another thread: it then fails
// with SatlinkErrorCancelled. settings are checked before dialling.
// listener, if not nil, hears every state change. A link that fails to come
// up and cannot hang up either fails with SatlinkErrorHangUpFailed, whose
// Reason says why it did not come up: the call may still be up. So does a
// handset that does not answer while a data call it may be relaying could
// not be ended. A fault inside the library while the link is set up
// (SatlinkErrorInternal) may leave a call up too, as whether one is up is
// then unknown: a transport that panics during the call setup has DTR
// dropped briefly, which ends the call after AT&D2 only where the DTR
// line reaches the handset. After either, an IridiumHandset made on the
// transport, while it is still open, ends the call: initialising it ends a
// data call left up, and its HangUp one held in online-command mode.
//
// The transport must not be used by an IridiumHandset meanwhile: for SMS
// and data on one handset, see EstablishLinkWithHandset.
func EstablishLink(transport ByteTransport, settings *LinkSettings, listener LinkListener, cancellation *Cancellation) (link *SatelliteLink, err error) {
	defer recoverPanic(&err)
	if transport == nil {
		return nil, nilArgument("transport")
	}
	if settings == nil {
		return nil, nilArgument("settings")
	}
	config, err := settings.config(cancellation)
	if err != nil {
		return nil, err
	}
	handset, err := initializedHandset(newForeignTransport(transport))
	if err != nil {
		return nil, err
	}
	return establishOwnLink(handset, config, listener)
}

// EstablishLinkWithHandset brings the link up with a handset the app also
// uses for SMS and status, unless cancellation, if not nil, stops it, as it
// stops EstablishLink. The link has the handset to itself until its call
// ends, however it ends: meanwhile the handset's methods, and another
// EstablishLinkWithHandset, fail with SatlinkErrorHandsetBusy. A link that
// fails to come up gives the handset back at once — unless it could not
// hang up either: it then fails with SatlinkErrorHangUpFailed, as
// EstablishLink does, and the handset is stranded until its Reinitialize
// succeeds. A fault inside the library while the link is set up
// (SatlinkErrorInternal) strands the handset too, as whether a call is up
// is then unknown.
func EstablishLinkWithHandset(handset *IridiumHandset, settings *LinkSettings, listener LinkListener, cancellation *Cancellation) (link *SatelliteLink, err error) {
	defer recoverPanic(&err)
	if handset == nil {
		return nil, nilArgument("handset")
	}
	if settings == nil {
		return nil, nilArgument("settings")
	}
	if _, err := made(handset.handset, "IridiumHandset"); err != nil {
		return nil, err
	}
	config, err := settings.config(cancellation)
	if err != nil {
		return nil, err
	}
	loan, err := handset.lend()
	if err != nil {
		return nil, err
	}
	defer loan.abandonUnlessSettled()
	established, err := satnet.Establish(handset.handset, config)
	if err != nil {
		loan.returnedAfter(err)
		return nil, establishError(err)
	}
	link = startLink(established, listener)
	loan.lentTo(link.watcher)
	return link, nil
}

// establishOwnLink brings a link up with a handset of its own, which is
// closed if the link does not come up, a panic included: a serial port it
// opened is released, and a ByteTransport stays the app's.
func establishOwnLink(handset *satlink.Handset, config satnet.Config, listener LinkListener) (*SatelliteLink, error) {
	started := false
	defer closeUnlessHandedOn(handset, &started)
	established, err := satnet.Establish(handset, config)
	if err != nil {
		return nil, establishError(err)
	}
	link := startLink(established, listener)
	started = true
	return link, nil
}

// startLink returns the app's handle on an established link, with
// listener, if not nil, hearing every change of its state.
func startLink(established *satnet.Link, listener LinkListener) *SatelliteLink {
	watcher := established.Watcher()
	// The listener's goroutine starts before dialling, so that failing to start
	// it costs no airtime; starting a goroutine cannot fail.
	if listener != nil {
		// The goroutine holds the watcher alone, never the link, so that a
		// link the app drops still hangs up.
		go labelled(listenerGoroutine, func() {
			deliverStateChanges(watcher, listener)
		})
	}
	return &SatelliteLink{link: established, watcher: watcher}
}

// Status returns the link's state, addresses and usage right now; never
// nil.
func (l *SatelliteLink) Status() (status *LinkStatus) {
	status = emptyLinkStatus()
	defer survivePanic("SatelliteLink.Status")
	if l.watcher == nil {
		// A zero SatelliteLink, which has no link to report on.
		return status
	}
	return linkStatusOf(l.watcher.Status())
}

// Resolve returns the IPv4 addresses of host, which the settings must
// allow, within timeoutMillis.
func (l *SatelliteLink) Resolve(host string, timeoutMillis int64) (addresses *StringList, err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return nil, err
	}
	resolved, err := link.Resolve(host, timeoutFromMillis(timeoutMillis))
	if err != nil {
		return nil, fromError(err)
	}
	return addressListOf(resolved), nil
}

// OpenTCP opens a TCP connection to host on port, which the settings must
// allow, within timeoutMillis. A port outside 0 to 65535 fails with
// SatlinkErrorInvalidArgument; port 0, which no rule allows, with
// SatlinkErrorPolicyDenied.
func (l *SatelliteLink) OpenTCP(host string, port int, timeoutMillis int64) (connection *TCPConnection, err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return nil, err
	}
	linkPort, err := portOf(port)
	if err != nil {
		return nil, err
	}
	conn, err := link.Dial(host, linkPort, timeoutFromMillis(timeoutMillis))
	if err != nil {
		return nil, fromError(err)
	}
	return &TCPConnection{conn: conn}, nil
}

// OpenTLS opens a TCP connection to host on port, as OpenTCP does, and
// secures it with TLS for that name, all within timeoutMillis. Reuse one
// connector: its session cache makes later handshakes several times
// cheaper.
func (l *SatelliteLink) OpenTLS(host string, port int, connector *TLSConnector, timeoutMillis int64) (connection *TLSConnection, err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return nil, err
	}
	if connector == nil {
		return nil, nilArgument("connector")
	}
	secure, err := made(connector.connector, "TLSConnector")
	if err != nil {
		return nil, err
	}
	linkPort, err := portOf(port)
	if err != nil {
		return nil, err
	}
	conn, err := link.DialTLS(host, linkPort, secure, timeoutFromMillis(timeoutMillis))
	if err != nil {
		return nil, fromError(err)
	}
	return &TLSConnection{conn: conn}, nil
}

// OpenUDP opens a UDP socket that exchanges datagrams with host on port
// only, resolving host within timeoutMillis; port as for OpenTCP.
func (l *SatelliteLink) OpenUDP(host string, port int, timeoutMillis int64) (connection *UDPConnection, err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return nil, err
	}
	linkPort, err := portOf(port)
	if err != nil {
		return nil, err
	}
	conn, err := link.DialUDP(host, linkPort, timeoutFromMillis(timeoutMillis))
	if err != nil {
		return nil, fromError(err)
	}
	return &UDPConnection{conn: conn}, nil
}

// HangUp closes the link: data already sent may still arrive, within the
// close grace, then the call is hung up. It waits up to timeoutMillis and
// returns the link's state, never nil: LinkStateClosed, with the Reason,
// once the call has ended; otherwise the hang-up goes on in the background
// and the state is the one the link is in meanwhile, normally
// LinkStateClosing. It fails with SatlinkErrorHangUpFailed if the hang-up
// could not be confirmed: the call may still be up. Allow for the close
// grace and the hang-up itself: 120000 milliseconds with the default
// settings.
func (l *SatelliteLink) HangUp(timeoutMillis int64) (state *LinkState, err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return nil, err
	}
	link.RequestClose()
	// Whether the call ended in time is in the status read next, which also
	// says whether its hang-up was confirmed.
	l.watcher.WaitClosed(timeoutFromMillis(timeoutMillis))
	status := l.watcher.Status()
	if status.State.Kind == satnet.LinkStateClosed && status.CallMayStillBeUp {
		return nil, hangUpFailed(status.State.Reason.String())
	}
	return linkStateOf(status.State), nil
}

// Close ends the call in the background, as HangUp does without waiting,
// as long as the transport stays open; after HangUp it has nothing left to
// do. Call it when done with the link rather than relying on the garbage
// collector. A borrowed handset stays with the link until the call has
// ended and then goes back by itself; if the hang-up failed, the handset's
// methods report SatlinkErrorHangUpFailed. A serial port the link opened
// itself (EstablishSerialLink) is closed once the call has ended. Close is
// idempotent.
func (l *SatelliteLink) Close() (err error) {
	defer recoverPanic(&err)
	link, err := made(l.link, "SatelliteLink")
	if err != nil {
		return err
	}
	link.RequestClose()
	return nil
}

// highestPort is the largest TCP or UDP port number.
const highestPort = 1<<16 - 1

// portOf returns port as a link's port number, refusing what no port
// number can be. Port 0 is left for the policy to refuse.
func portOf(port int) (uint16, error) {
	if port < 0 || port > highestPort {
		return 0, invalidArgument("port must be between 0 and 65535")
	}
	return uint16(port), nil
}
