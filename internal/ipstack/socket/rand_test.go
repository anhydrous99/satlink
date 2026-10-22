package socket

import (
	"slices"
	"testing"
)

// draw returns the first four values next returns from a generator seeded
// with seed.
func draw[T any](seed uint64, next func(*Rand) T) []T {
	random := Rand{state: seed}
	values := make([]T, 4)
	for i := range values {
		values[i] = next(&random)
	}
	return values
}

// The golden values are smoltcp's outputs for these seeds.
func TestRandGoldenValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		seed            uint64
		wantUint32      []uint32
		wantUint16      []uint16
		wantSourcePorts []uint16
	}{
		{
			seed:            0,
			wantUint32:      []uint32{0x3, 0xfdecf8a2, 0x6130bec3, 0x77368c50},
			wantUint16:      []uint16{0x3, 0x54e, 0xdff3, 0xfb66},
			wantSourcePorts: []uint16{1358, 57331, 64358, 18633},
		},
		{
			seed:            0x5EED,
			wantUint32:      []uint32{0xaf7268a4, 0x9ee15bb3, 0x55558f4e, 0xd42f480d},
			wantUint16:      []uint16{0xc7d6, 0xc552, 0xda1b, 0x9c22},
			wantSourcePorts: []uint16{51158, 50514, 55835, 39970},
		},
	}
	for _, c := range cases {
		if got := draw(c.seed, (*Rand).Uint32); !slices.Equal(got, c.wantUint32) {
			t.Errorf("seed %#x: Uint32 = %#x, want %#x", c.seed, got, c.wantUint32)
		}
		if got := draw(c.seed, (*Rand).Uint16); !slices.Equal(got, c.wantUint16) {
			t.Errorf("seed %#x: Uint16 = %#x, want %#x", c.seed, got, c.wantUint16)
		}
		if got := draw(c.seed, (*Rand).SourcePort); !slices.Equal(got, c.wantSourcePorts) {
			t.Errorf("seed %#x: SourcePort = %d, want %d", c.seed, got, c.wantSourcePorts)
		}
	}
}

// SourcePort skips the reserved ports up to 1024 and takes 1025: the first
// Uint16 of these seeds is 3, 1024 and 1025.
func TestRandSourcePortIsTheFirstDrawAbove1024(t *testing.T) {
	t.Parallel()
	cases := []struct {
		seed            uint64
		wantFirstUint16 uint16
		wantPort        uint16
		// wantNextUint16 is the draw after the one SourcePort returned.
		wantNextUint16 uint16
	}{
		{seed: 0, wantFirstUint16: 3, wantPort: 1358, wantNextUint16: 57331},
		{seed: 0x1b705, wantFirstUint16: 1024, wantPort: 45842, wantNextUint16: 6677},
		{seed: 0x28689, wantFirstUint16: 1025, wantPort: 1025, wantNextUint16: 35777},
	}
	for _, c := range cases {
		if first := draw(c.seed, (*Rand).Uint16)[0]; first != c.wantFirstUint16 {
			t.Fatalf("seed %#x: the first Uint16 is %d, want %d", c.seed, first, c.wantFirstUint16)
		}
		random := Rand{state: c.seed}

		port := random.SourcePort()

		if next := random.Uint16(); port != c.wantPort || next != c.wantNextUint16 {
			t.Errorf("seed %#x: SourcePort() = %d, then Uint16() = %d; want %d, then %d", c.seed, port, next, c.wantPort, c.wantNextUint16)
		}
	}
}

func TestContextRandIsSeededAndShared(t *testing.T) {
	t.Parallel()
	cx := NewContext(epoch, 0x5EED, 1500)

	first := cx.Rand().Uint32()
	second := cx.Rand().Uint32()

	if first != 0xaf7268a4 || second != 0x9ee15bb3 {
		t.Errorf("draws %#x, %#x; want the seed's sequence 0xaf7268a4, 0x9ee15bb3", first, second)
	}
}

func TestZeroRandIsSeededWithZero(t *testing.T) {
	t.Parallel()
	var random Rand

	if first, second := random.Uint32(), random.Uint32(); first != 0x3 || second != 0xfdecf8a2 {
		t.Errorf("the zero Rand draws %#x, %#x; want seed 0's 0x3, 0xfdecf8a2", first, second)
	}
}
