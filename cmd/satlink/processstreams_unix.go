//go:build unix

package main

import "syscall"

// badDescriptor is the error of a standard stream whose descriptor is not
// open for the direction: EBADF, as Rust's standard library has it on Unix
// (is_ebadf).
const badDescriptor = syscall.EBADF
