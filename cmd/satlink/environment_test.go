package main

import (
	"slices"
	"testing"
)

// The run sees the process through its environment, which tests build
// themselves: what the process hands over must reach it. What such a
// stream reads and writes, processstreams_test.go shows, and which streams
// are terminals, terminal_pty_test.go.
func TestTheProcessesArgumentsStreamsAndVariablesReachTheRun(t *testing.T) {
	stdin, stdout, stderr := openNullDevice(t), openNullDevice(t), openNullDevice(t)
	args := []string{"--simulate=instant", "info"}
	var looked []string
	lookupEnv := func(key string) (string, bool) {
		looked = append(looked, key)
		return "/dev/ttyACM7", true
	}

	env := environmentOf(args, standardFiles{stdin: stdin, stdout: stdout, stderr: stderr}, lookupEnv)

	if env.stdin != (processStream{file: stdin}) || env.stdout != (processStream{file: stdout}) ||
		env.stderr != (processStream{file: stderr}) {
		t.Errorf("the streams are %v, %v and %v; want the process's, as processStreams", env.stdin, env.stdout, env.stderr)
	}
	if !slices.Equal(env.args, args) {
		t.Errorf("the arguments are %q, want %q", env.args, args)
	}
	value, set := env.lookupEnv(portVariable)
	if value != "/dev/ttyACM7" || !set || !slices.Equal(looked, []string{portVariable}) {
		t.Errorf("%s is %q (set %t), having looked up %q; want the process's variable", portVariable, value, set, looked)
	}
}
