package main

// ignoreBrokenPipes does nothing on Windows, which has no SIGPIPE: a write
// to a pipe that nobody reads fails there already.
func ignoreBrokenPipes() {}
