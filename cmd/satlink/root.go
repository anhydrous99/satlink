package main

import "io"

// programName is the command's name, as its help and its hang-up warning
// give it.
const programName = "satlink"

// version is satlink's version.
const version = "0.1.0"

// cli is one run of the command line: the world it runs in, its standard
// streams, the options every command shares, which reading the command line
// fills in before a command runs, its log, and the interruptions it catches
// during a net call.
type cli struct {
	env           environment
	streams       streams
	options       globalOptions
	log           *stderrLog
	interruptions interruptions
}

// newCLI returns one run of the command line in env. Errors and logs go to
// stderr, which must be safe for concurrent use; the command's own lines
// follow the log lines made before them.
func newCLI(env environment, stderr io.Writer) *cli {
	log := newStderrLog(stderr)
	return &cli{
		env: env,
		streams: streams{stdin: env.stdin, stdout: env.stdout, stderr: log.writerBehind(),
			stdinIsTerminal: env.stdinIsTerminal},
		log: log,
	}
}

// rootCommand returns the command tree of the run, whose arguments the run's
// options and the commands' own variables keep.
func (c *cli) rootCommand() *command {
	root := newRoot(programName, "Talk to an Iridium satellite handset over USB", c.options.arguments()...)
	return root.withSubcommands(
		c.listCommand(), c.infoCommand(), c.statusCommand(), c.smsCommand(),
		c.dialCommand(), c.answerCommand(), c.hangUpCommand(), c.atCommand(),
		c.monitorCommand(), c.netCommand(),
	)
}

// versionText is what -V prints: "satlink 0.1.0".
func versionText() string {
	return programName + " " + version + "\n"
}

// installLogger sends log records to standard error, from the level the -v
// flags ask for.
func (c *cli) installLogger() {
	c.env.installLogger(c.log.handler(c.options.verbosity))
}
