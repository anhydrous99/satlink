//go:build unix

package main

import "syscall"

// badDescriptor is the error of a standard stream whose descriptor is not
// open for the direction: EBADF on Unix.
const badDescriptor = syscall.EBADF
