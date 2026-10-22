package sim

// What the simulated handset has seen, for tests and status displays.

// Stats are the counters and records the simulated handset keeps, read
// through [Control.Stats].
type Stats struct {
	// CallsPlaced counts the calls dialled with ATD, voice and data alike.
	CallsPlaced int
	// CallsConnected counts the calls that were answered: voice calls at
	// once, data calls when the handset reported CONNECT.
	CallsConnected int
	// InDataCall reports whether a data call is up, in online-data or
	// online-command mode.
	InDataCall bool
	// DataBytesFromApp counts the online-data bytes the app wrote, not
	// counting an escape sequence.
	DataBytesFromApp uint64
	// DataBytesToApp counts the online-data bytes delivered to the app.
	DataBytesToApp uint64
	// DiscardedBytes counts the bytes the discard service (TCP port 9) has
	// received.
	DiscardedBytes uint64
	// SMSSent are the messages submitted with AT+CMGS, oldest first.
	SMSSent []SentSMS
}

// SentSMS is a message the app submitted with AT+CMGS.
type SentSMS struct {
	// Number is the recipient.
	Number string
	// Text is the message text.
	Text string
}
