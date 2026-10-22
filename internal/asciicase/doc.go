// Package asciicase maps and compares the case of ASCII letters alone, as
// Rust's to_ascii_lowercase, to_ascii_uppercase and eq_ignore_ascii_case
// do. The Rust version compared AT commands and their echoes, HTTP header
// names and host names so, and matched the simulated handset's commands.
//
// Go's strings.ToLower, strings.ToUpper and strings.EqualFold follow
// Unicode, which maps some letters beyond ASCII onto ASCII ones: 'ſ'
// (U+017F) upper-cases and folds to 'S', 'ı' (U+0131) upper-cases to 'I',
// and the Kelvin sign (U+212A) lower-cases and folds to 'k'. Text that is
// no command, header name or host name would then pass for one. The
// functions here change 'A' to 'Z' and 'a' to 'z' alone, and leave every
// other byte as it is, UTF-8 or not.
package asciicase
