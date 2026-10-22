package main

import "syscall"

// badDescriptor is the error of a standard stream whose handle is NULL: in
// a process without a console, or one started by such a process, a stream
// it was not handed. It is ERROR_INVALID_HANDLE (winerror.h), which package
// syscall does not name; syscall.EBADF is a number Go made up there, which no
// call returns. It is not taken from golang.org/x/sys/windows, which this
// module does not import directly. A stream opened for the other direction
// fails with ERROR_ACCESS_DENIED instead, which stays a failure.
const badDescriptor syscall.Errno = 6
