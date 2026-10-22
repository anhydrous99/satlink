package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
)

// The exit statuses, as the Rust version's: clap's for a command line that
// does not parse, and the Rust runtime's for a panic. Go's runtime would end
// a panicking program with clap's status instead.
const (
	exitSuccess  = 0
	exitFailure  = 1
	exitUsage    = 2
	exitPanicked = 101
)

// run carries out the command line that env holds and returns the exit
// status: exitSuccess, exitFailure when the command failed, exitUsage when
// the command line does not parse, exitInterrupted when an interruption
// asked a net call to stop, or exitPanicked when the run panicked. The
// command line is read as clap read it; cobra then runs the command it
// names. Every call builds a fresh command tree, so runs share nothing.
func run(ctx context.Context, env environment) (status int) {
	// Log lines come from the link's goroutines while commands write.
	stderr := &syncWriter{w: env.stderr}
	c := newCLI(env, stderr)
	defer c.log.close()
	defer func() {
		// The handset is closed by now, as the Rust version's was dropped
		// while its panic unwound.
		if value := recover(); value != nil {
			status = c.panicked(value)
		}
	}()
	root := c.rootCommand()
	// Help and errors in clap's styles, which endBeforeRunning takes out for
	// a stream that shows none.
	clapStyles := palette{styled: true}
	named, exit := parseCommandLine(root, env.args, surroundings{lookupEnv: env.lookupEnv, palette: clapStyles})
	if exit != nil {
		return endBeforeRunning(exit, env, stderr)
	}
	// Only the command's path is left for cobra to find it by. A copy, never
	// nil: given nil, cobra would read os.Args instead.
	root.runner.SetArgs(append([]string{}, named.path()...))
	return c.finish(root.runner.ExecuteContext(ctx))
}

// finish ends a run whose command returned err, and returns the exit
// status. The log lines still waiting get a moment to reach standard error
// before the error that ends the run, and an interruption makes the status
// exitInterrupted, whatever the command's outcome.
func (c *cli) finish(err error) int {
	c.log.flush()
	if err != nil {
		// Best effort: standard error may be the reason it failed.
		_, _ = fmt.Fprintf(c.streams.stderr, "error: %v\n", err)
	}
	c.interruptions.stop()
	switch {
	case c.interruptions.askedToStop():
		return exitInterrupted
	case err != nil:
		return exitFailure
	default:
		return exitSuccess
	}
}

// panicked ends a run that panicked with value, as the Rust version ended
// one: the panic is reported, and the status is exitPanicked, whatever an
// interruption asked for.
func (c *cli) panicked(value any) int {
	reportPanic(c.streams.stderr, value)
	c.interruptions.stop()
	return exitPanicked
}

// reportPanic writes a recovered panic and where it happened to stderr, as
// Rust's panic hook did.
func reportPanic(stderr io.Writer, value any) {
	// Best effort: what follows — the hang-up, or the end of the run —
	// matters more.
	_, _ = fmt.Fprintf(stderr, "panic: %v\n\n%s\n", value, debug.Stack())
}

// endBeforeRunning prints what a command line that runs no command calls
// for — help or the version on env's standard output, or why it does not
// parse on stderr — and returns the exit status. A stream that shows no
// styles gets the text as anstream's StripStream passed it on to one.
func endBeforeRunning(exit *commandLineExit, env environment, stderr io.Writer) int {
	out, isTerminal := stderr, env.stderrIsTerminal
	if exit.status == exitSuccess {
		out, isTerminal = env.stdout, env.stdoutIsTerminal
	}
	text := exit.text
	if !showsStyles(isTerminal, env.lookupEnv) {
		text = stripStyles(text)
	}
	// Best effort, as clap's: the stream may be a closed pipe, and the exit
	// status says the rest.
	_, _ = io.WriteString(out, text)
	return exit.status
}
