package main

import "syscall"

// readTerminalSettings is the ioctl request that reads a terminal's
// settings, and fails on any other file: TCGETS, as glibc's isatty uses.
const readTerminalSettings = syscall.TCGETS
