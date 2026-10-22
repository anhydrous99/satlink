//go:build unix

package errnotext

import (
	"syscall"
	"testing"
)

func TestTextIsTheCLibrarysText(t *testing.T) {
	tests := []struct {
		errno syscall.Errno
		want  string
	}{
		{syscall.ENOENT, "No such file or directory"},
		{syscall.EPIPE, "Broken pipe"},
		{syscall.EISDIR, "Is a directory"},
	}
	for _, test := range tests {
		if got := Text(test.errno); got != test.want {
			t.Errorf("Text(%d) = %q, want %q", int(test.errno), got, test.want)
		}
	}
}
