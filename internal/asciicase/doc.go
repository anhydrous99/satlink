// Package asciicase maps and compares the case of ASCII letters alone:
// Lower, Upper and EqualFold leave every other byte as it is. AT commands
// and their echoes, HTTP header names and host names are compared so, and
// the simulated handset matches its commands so.
//
// Go's strings.ToLower, strings.ToUpper and strings.EqualFold follow
// Unicode, which maps some letters beyond ASCII onto ASCII ones: 'ſ'
// (U+017F) upper-cases and folds to 'S', 'ı' (U+0131) upper-cases to 'I',
// and the Kelvin sign (U+212A) lower-cases and folds to 'k'. Text that is
// no command, header name or host name would then pass for one. The
// functions here change 'A' to 'Z' and 'a' to 'z' alone, and leave every
// other byte as it is, UTF-8 or not.
package asciicase
