// Package errnotext writes an error number from the operating system as
// the C library's strerror(3) writes it, which is how the Rust version
// reported such errors: its serial library passed on the C library's texts,
// and Rust's io::Error writes them before the number ("Broken pipe (os
// error 32)").
//
// Go's own texts are the C library's but for their first letter, which Go
// writes in lower case ("no such file or directory"); [Text] writes it as a
// capital again.
package errnotext
