package main

import (
	"io"
	"log/slog"
	"os"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/serial"
	"github.com/anhydrous99/satlink/sim"
)

// environment is what one run of the command sees of the world. main hands
// over the process's own; tests hand over buffers, variables and handsets of
// their own, so that commands run in-process.
type environment struct {
	// args are the command-line arguments, without the program's name.
	args []string
	// stdin, stdout and stderr are the standard streams.
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// stdoutIsTerminal and stderrIsTerminal say which of the streams are
	// terminals, where help and errors are styled; stdinIsTerminal says
	// whether someone may be typing standard input.
	stdinIsTerminal, stdoutIsTerminal, stderrIsTerminal bool
	// lookupEnv returns an environment variable's value, and whether it is
	// set: one set to "" counts, as it did for clap.
	lookupEnv func(key string) (string, bool)
	// installLogger makes handler the destination of every package's log
	// records: slog.SetDefault for the process.
	installLogger func(handler slog.Handler)
	// discoverHandsets lists the handsets on USB, for list and for a
	// command given no port: serial.Discover.
	discoverHandsets func() ([]serial.Port, error)
	// simulateHandset returns a simulated handset made with config: sim.New.
	simulateHandset func(config sim.Config) satlink.Transport
	// notifyInterruptions relays Ctrl-C, SIGTERM and SIGHUP to signals
	// instead of letting them end the process, until the function it
	// returns is called: notifyInterruptions for the process.
	notifyInterruptions func(signals chan<- os.Signal) (stop func())
	// quitInterrupted ends the process at once, as an unhandled Ctrl-C
	// does: dieInterrupted.
	quitInterrupted func()
}

// processEnvironment returns the environment of this process, with its own
// arguments, standard streams and variables.
func processEnvironment() environment {
	return environmentOf(os.Args[1:], standardFiles{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, os.LookupEnv)
}

// standardFiles are the files of a process's standard streams.
type standardFiles struct {
	stdin, stdout, stderr *os.File
}

// environmentOf returns the environment of a process run with args, whose
// standard streams are files and whose variables lookupEnv finds: the
// streams read and written as processStreams, each said to be a terminal
// when it is one, the default logger, real handsets, or simulated ones on
// request, and the process's signals.
func environmentOf(args []string, files standardFiles, lookupEnv func(string) (string, bool)) environment {
	return environment{
		args:             args,
		stdin:            processStream{file: files.stdin},
		stdout:           processStream{file: files.stdout},
		stderr:           processStream{file: files.stderr},
		stdinIsTerminal:  isTerminal(files.stdin),
		stdoutIsTerminal: isTerminal(files.stdout),
		stderrIsTerminal: isTerminal(files.stderr),
		lookupEnv:        lookupEnv,
		installLogger: func(handler slog.Handler) {
			slog.SetDefault(slog.New(handler))
		},
		discoverHandsets: serial.Discover,
		simulateHandset: func(config sim.Config) satlink.Transport {
			return sim.New(config)
		},
		notifyInterruptions: notifyInterruptions,
		quitInterrupted:     dieInterrupted,
	}
}
