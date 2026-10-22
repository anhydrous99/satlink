package sim

import (
	"bytes"
	"slices"
	"testing"
)

func pushAll(assembler *lineAssembler, input []byte) []line {
	var lines []line
	for _, b := range input {
		if finished, ok := assembler.push(b); ok {
			lines = append(lines, finished)
		}
	}
	return lines
}

func TestCarriageReturnEndsALineAndLineFeedIsIgnored(t *testing.T) {
	t.Parallel()
	var assembler lineAssembler

	lines := pushAll(&assembler, []byte("AT\r\nAT+CSQ\r"))

	if want := []line{{text: "AT"}, {text: "AT+CSQ"}}; !slices.Equal(lines, want) {
		t.Errorf("lines %+v; want %+v", lines, want)
	}
}

func TestAnOverlongLineIsReportedOnceAndForgotten(t *testing.T) {
	t.Parallel()
	var assembler lineAssembler
	input := append(bytes.Repeat([]byte("A"), maxLineLen+1), "\rAT\r"...)

	lines := pushAll(&assembler, input)

	if want := []line{{tooLong: true}, {text: "AT"}}; !slices.Equal(lines, want) {
		t.Errorf("lines %+v; want %+v", lines, want)
	}
}

func TestInvalidUTF8IsReplaced(t *testing.T) {
	t.Parallel()
	var assembler lineAssembler

	lines := pushAll(&assembler, []byte("AT\xff\r"))

	if want := []line{{text: "AT\uFFFD"}}; !slices.Equal(lines, want) {
		t.Errorf("lines %+v; want %+v", lines, want)
	}
}
