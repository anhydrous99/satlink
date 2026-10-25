package storage

import (
	"errors"
	"iter"
	"strconv"
	"strings"
)

// AssemblerMaxSegmentCount is how many separate runs of data an Assembler
// tracks. Out-of-order data that would need a further run is dropped, and the
// peer sends it again.
const AssemblerMaxSegmentCount = 4

// ErrTooManyHoles reports data that would need more runs than an Assembler
// tracks.
var ErrTooManyHoles = errors.New("too many holes")

// Assembler records which parts of a buffer have been filled, as offsets from
// its start: a TCP socket uses one to accept segments out of order.
//
// The zero Assembler is empty and ready to use.
type Assembler struct {
	// contigs describe the buffer from its start, each a hole followed by
	// data. The contigs in use come first, and every one of them has data;
	// all but the first have a hole too (adjacent runs are merged). The
	// remaining contigs are empty.
	contigs [AssemblerMaxSegmentCount]contig
}

// contig is a hole, a run of absent data, followed by a run of present data.
type contig struct {
	holeSize int
	dataSize int
}

// IsEmpty reports whether no data is recorded.
func (a *Assembler) IsEmpty() bool {
	return !a.contigs[0].hasData()
}

// Add records size octets at offset, merging them with the runs they touch,
// or returns ErrTooManyHoles and records nothing.
func (a *Assembler) Add(offset, size int) error {
	if size == 0 {
		return nil
	}
	index, offset := a.locate(offset)
	switch {
	case index == len(a.contigs):
		// The data lies beyond every run, and there is no contig left.
		return ErrTooManyHoles
	case !a.contigs[index].hasData():
		// The data lies beyond every run: it becomes the last one.
		a.contigs[index] = contig{holeSize: offset, dataSize: size}
		return nil
	case offset+size < a.contigs[index].holeSize:
		return a.insertWithinHole(index, offset, size)
	}
	if offset < a.contigs[index].holeSize {
		// The data covers the end of the hole and reaches into the run.
		a.contigs[index] = a.contigs[index].withHoleOf(offset)
	}
	end := offset + size
	a.mergeFollowingContigs(index, end)
	a.extendData(index, end)
	return nil
}

// RemoveFront removes the run at the very start of the buffer and returns its
// length; 0 when the buffer starts with a hole.
func (a *Assembler) RemoveFront() int {
	front := a.contigs[0]
	if front.hasHole() || !front.hasData() {
		return 0
	}
	a.removeContigs(0, 1)
	return front.dataSize
}

// AddThenRemoveFront does Add and then RemoveFront, except that it never
// fails for data at offset 0: TCP must never drop the next expected segment,
// or the connection could stall.
func (a *Assembler) AddThenRemoveFront(offset, size int) (int, error) {
	// Data at offset 0 that ends within the first hole is the only kind that
	// would need another contig; it is removed again at once, so only the
	// hole shrinks.
	if offset == 0 && size < a.contigs[0].holeSize {
		a.contigs[0].holeSize -= size
		return size, nil
	}
	if err := a.Add(offset, size); err != nil {
		return 0, err
	}
	return a.RemoveFront(), nil
}

// DataRanges returns the recorded runs in order, as (start, end) offsets
// from the start of the buffer, end exclusive: data of 100 octets, a hole of
// 200 and data of 100 yield (0, 100) and (300, 400).
func (a *Assembler) DataRanges() iter.Seq2[int, int] {
	return func(yield func(start, end int) bool) {
		end := 0
		for _, c := range a.contigs {
			start := end + c.holeSize
			end = start + c.dataSize
			if start < end && !yield(start, end) {
				return
			}
		}
	}
}

// String formats the runs: holes in parentheses and data
// lengths, as in "[ 100 (200) 100 ]".
func (a *Assembler) String() string {
	var text strings.Builder
	text.WriteString("[ ")
	for _, c := range a.contigs {
		if !c.hasData() {
			break
		}
		text.WriteString(c.String() + " ")
	}
	text.WriteString("]")
	return text.String()
}

// locate returns the index of the first contig that is empty or whose hole
// or data contains offset (or ends at it), with offset made relative to the
// start of that contig. The index is len(a.contigs) when every contig is in
// use and ends before offset.
func (a *Assembler) locate(offset int) (int, int) {
	for index, c := range a.contigs {
		if !c.hasData() || offset <= c.totalSize() {
			return index, offset
		}
		offset -= c.totalSize()
	}
	return len(a.contigs), offset
}

// insertWithinHole records data that lies entirely within the hole of the
// contig at index, splitting the hole in two. offset is relative to the
// start of that contig.
func (a *Assembler) insertWithinHole(index, offset, size int) error {
	if a.contigs[len(a.contigs)-1].hasData() {
		return ErrTooManyHoles
	}
	copy(a.contigs[index+1:], a.contigs[index:])
	a.contigs[index] = contig{holeSize: offset, dataSize: size}
	a.contigs[index+1].holeSize -= offset + size
	return nil
}

// mergeFollowingContigs merges into the contig at index every following
// contig whose hole the new data, ending at end (relative to the start of the
// contig at index), fills completely.
func (a *Assembler) mergeFollowingContigs(index, end int) {
	next := index + 1
	for next < len(a.contigs) && a.contigs[next].hasData() &&
		end >= a.contigs[index].totalSize()+a.contigs[next].holeSize {
		a.contigs[index].dataSize += a.contigs[next].totalSize()
		next++
	}
	a.removeContigs(index+1, next-index-1)
}

// extendData grows the data of the contig at index to end (relative to the
// start of that contig) and shrinks the next contig's hole by as much.
func (a *Assembler) extendData(index, end int) {
	growth := end - a.contigs[index].totalSize()
	if growth <= 0 {
		return
	}
	a.contigs[index].dataSize += growth
	if next := index + 1; next < len(a.contigs) && a.contigs[next].hasData() {
		a.contigs[next].holeSize -= growth
	}
}

// removeContigs removes count contigs from index on, moving the following
// ones forward and leaving empty contigs at the end.
func (a *Assembler) removeContigs(index, count int) {
	copy(a.contigs[index:], a.contigs[index+count:])
	clear(a.contigs[len(a.contigs)-count:])
}

func (c contig) hasHole() bool {
	return c.holeSize != 0
}

func (c contig) hasData() bool {
	return c.dataSize != 0
}

func (c contig) totalSize() int {
	return c.holeSize + c.dataSize
}

// withHoleOf returns the contig with a hole of size, the rest of its length
// data.
func (c contig) withHoleOf(size int) contig {
	return contig{holeSize: size, dataSize: c.totalSize() - size}
}

// String formats the contig: the hole in parentheses, then
// the data length.
func (c contig) String() string {
	var parts []string
	if c.hasHole() {
		parts = append(parts, "("+strconv.Itoa(c.holeSize)+")")
	}
	if c.hasData() {
		parts = append(parts, strconv.Itoa(c.dataSize))
	}
	return strings.Join(parts, " ")
}
