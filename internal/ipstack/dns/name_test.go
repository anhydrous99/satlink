package dns

import (
	"bytes"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// errBrokenName is the error of the names brokenNameOf returns.
var errBrokenName = errors.New("broken name")

// labelsOf returns a name's labels as wire.DNSPacket.NameLabels yields them.
func labelsOf(labels ...string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for _, label := range labels {
			if !yield([]byte(label), nil) {
				return
			}
		}
	}
}

// brokenNameOf returns a name that yields labels, then errBrokenName, as
// wire.DNSPacket.NameLabels yields a malformed name.
func brokenNameOf(labels ...string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for label := range labelsOf(labels...) {
			if !yield(label, nil) {
				return
			}
		}
		yield(nil, errBrokenName)
	}
}

func TestNamesEqualComparesLabelsInStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		a, b      iter.Seq2[[]byte, error]
		wantEqual bool
		wantErr   error
	}{
		{"the same labels", labelsOf("example", "com"), labelsOf("example", "com"), true, nil},
		{"two root names", labelsOf(), labelsOf(), true, nil},
		{"a different label", labelsOf("example", "com"), labelsOf("example", "org"), false, nil},
		{"labels in another case", labelsOf("Example", "COM"), labelsOf("example", "com"), true, nil},
		{"letters beyond ASCII in another case", labelsOf("\u00c9cole"), labelsOf("\u00e9cole"), false, nil},
		{"the first name shorter", labelsOf("example"), labelsOf("example", "com"), false, nil},
		{"the second name shorter", labelsOf("www", "example"), labelsOf("www"), false, nil},
		{"an empty label against the end", labelsOf("example", ""), labelsOf("example"), false, nil},
		{"the first name broken", brokenNameOf("example"), labelsOf("example", "com"), false, errBrokenName},
		{"the second name broken", labelsOf("example", "com"), brokenNameOf("example"), false, errBrokenName},
		{"a difference before a break", brokenNameOf("www"), labelsOf("example", "com"), false, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			equal, err := namesEqual(test.a, test.b)

			if equal != test.wantEqual || !errors.Is(err, test.wantErr) {
				t.Errorf("namesEqual = %t, %v; want %t, %v", equal, err, test.wantEqual, test.wantErr)
			}
		})
	}
}

func TestCopyNameWritesTheWireForm(t *testing.T) {
	t.Parallel()
	name := []byte("\x03old\x00")

	err := copyName(&name, labelsOf("www", "example", "net"))

	if err != nil || !bytes.Equal(name, targetName) {
		t.Errorf("copyName = %q, %v; want %q", name, err, targetName)
	}
}

func TestCopyNameLeavesTheNameAsItWasOnAFailure(t *testing.T) {
	t.Parallel()
	label63 := strings.Repeat("a", maxLabelLen)
	label62 := label63[:maxLabelLen-1]
	tests := []struct {
		name    string
		labels  iter.Seq2[[]byte, error]
		wantErr error
	}{
		{"a broken name", brokenNameOf("www", "example"), errBrokenName},
		// 3 × 64 octets leave 63 for the fourth label: its length octet
		// fits, its 63 octets do not.
		{"a label over the size limit", labelsOf(label63, label63, label63, label63), wire.ErrMalformed},
		{"no room for the root label", labelsOf(label63, label63, label63, label62), wire.ErrMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name := append([]byte(nil), exampleName...)

			err := copyName(&name, test.labels)

			if !errors.Is(err, test.wantErr) || !bytes.Equal(name, exampleName) {
				t.Errorf("copyName = %q, %v; want %q, %v", name, err, exampleName, test.wantErr)
			}
		})
	}
}

func TestEncodeNameWritesTheWireForm(t *testing.T) {
	t.Parallel()

	encoded, err := encodeName("www.example.net")

	if err != nil || !bytes.Equal(encoded, targetName) {
		t.Errorf("encodeName = %q, %v; want %q", encoded, err, targetName)
	}
}
