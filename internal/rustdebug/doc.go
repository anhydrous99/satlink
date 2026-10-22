// Package rustdebug writes values as the Rust version's Debug formatting
// wrote them, for the messages that keep the Rust version's texts.
//
// Rust's messages quote a string with {:?} — a command, a phone number, a
// host name, a rule, a URL — and Quote writes it as Rust did, where Go's %q
// would escape control characters otherwise ("\v" for Rust's "\u{b}"). The
// module's other messages quote with it too, so that a message of its own
// reads as the Rust ones beside it.
//
// Rust logged "IPv4 up: {settings:?}" in satnet's driver and in the
// simulator's gateway; with IPv4Settings both Go lines read as the Rust ones
// did, where Go's %+v would write "{LocalAddress:10.64.0.2 …}".
//
// The package imports nothing of the module's, so that every package,
// the core included, can use it.
package rustdebug
