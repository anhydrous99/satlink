// Package debugtext writes values in the Debug style that satlink's messages
// use: strings quoted with the escapes a Debug print produces, and structs
// printed with their field names.
//
// Messages quote a command, a phone number, a host name, a rule or a URL with
// Quote, which writes control characters as "\u{b}" where Go's %q would write
// "\v". Using one quoting rule for every message keeps them alike.
//
// IPv4Settings writes the settings PPP negotiated as "Ipv4Settings { … }",
// with Option and list notation for an absent peer address and for the DNS
// servers, so that a log line about the link reads the same wherever it is
// written, where Go's %+v would write "{LocalAddress:10.64.0.2 …}".
//
// The package imports nothing of the module's, so that every package,
// the core included, can use it.
package debugtext
