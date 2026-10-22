// Package errnotext writes an error number from the operating system as the
// C library's strerror(3) writes it, such as "Broken pipe" for EPIPE.
//
// Go's own texts are the C library's but for their first letter, which Go
// writes in lower case ("no such file or directory"); [Text] writes it as a
// capital again.
package errnotext
