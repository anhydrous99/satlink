package main

import "syscall"

// badDescriptor is the error of a standard stream whose handle is NULL: in
// a process without a console, or one started by such a process, a stream
// it was not handed. It is ERROR_INVALID_HANDLE (winerror.h), which package
// syscall does not name, as Rust's standard library has it on Windows
// (is_ebadf); syscall.EBADF is a number Go made up there, which no call
// returns. A stream opened for the other direction fails with
// ERROR_ACCESS_DENIED instead, which stays a failure, as in Rust.
const badDescriptor syscall.Errno = 6
