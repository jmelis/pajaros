// Package seasonal is the bit-packed encoding of one hotspot's species
// records by calendar month — the value format of the species_by_hotspot
// bucket in hotspots_seasonal.bolt. cmd/gensnapshot writes it, the server reads it;
// both go through this package so the two sides cannot drift apart.
//
// A blob holds, for every species seen at the hotspot, a count per calendar
// month (January..December, years pooled), exactly. Counts and species IDs
// are small numbers, so values are written as bit strings rather than whole
// bytes: IDs of a sorted list as Rice-coded gaps, counts as Rice or
// Elias-gamma codes, month presence as a 12-bit mask. Bits are packed MSB
// first and the blob is zero-padded to a byte.
//
// Layout of a blob:
//
//	1 bit   layout: 0 = month-major, 1 = species-major
//	4 bits  kg: Rice parameter for species-ID gaps
//	3 bits  kc: Rice parameter for counts-1, or 7 for Elias-gamma(count)
//
//	month-major:   12-bit mask of months with data; then for each such
//	               month: gamma(species in month), then per species (IDs
//	               ascending) rice(gap, kg) and the count.
//	species-major: gamma(species count); then per species (IDs ascending)
//	               rice(gap, kg); a 1-bit "single month" flag; if set a 4-bit
//	               month (0..11) and the count; if clear a 12-bit mask and
//	               one count per set bit.
//
// The encoder emits whichever layout is smaller for the hotspot. A gap is
// id - previousID - 1 (previousID starts at -1). Gamma(x) is x.bitLen()-1
// zeros followed by x in binary (x >= 1). Rice(x, k) is x>>k in unary (that
// many ones, then a zero) followed by the low k bits of x.
package seasonal

import (
	"errors"
	"math/bits"
	"sort"
)

// FileName is the bbolt file that carries these blobs. It differs from the
// earlier all-time hotspots.bolt so both can sit in one data directory while
// a rollout is in flight: the old server keeps reading the old file.
const FileName = "hotspots_seasonal.bolt"

// Months is a species' record count per calendar month, index 0 = January.
type Months [12]uint32

// Entry is one species at a hotspot.
type Entry struct {
	ID     uint16
	Months Months
}

// Total returns the species' count over all twelve months.
func (m *Months) Total() int {
	t := 0
	for _, c := range m {
		t += int(c)
	}
	return t
}

const (
	gammaCounts = 7 // kc value meaning "counts are Elias-gamma coded"
	maxKG       = 12
)

var errCorrupt = errors.New("seasonal: corrupt or truncated blob")

// ---- bit I/O ----------------------------------------------------------

type bitWriter struct {
	buf []byte
	acc uint64
	n   uint // bits held in acc
}

// put appends the low nb bits of v (nb <= 32).
func (w *bitWriter) put(v uint64, nb uint) {
	w.acc = w.acc<<nb | v&(1<<nb-1)
	w.n += nb
	for w.n >= 8 {
		w.n -= 8
		w.buf = append(w.buf, byte(w.acc>>w.n))
	}
}

func (w *bitWriter) ones(q uint64) {
	for ; q >= 32; q -= 32 {
		w.put(1<<32-1, 32)
	}
	w.put(1<<q-1, uint(q))
}

func (w *bitWriter) gamma(x uint64) {
	l := uint(bits.Len64(x))
	w.put(0, l-1)
	if l > 32 {
		w.put(x>>32, l-32)
		w.put(x, 32)
		return
	}
	w.put(x, l)
}

func (w *bitWriter) rice(x uint64, k uint) {
	w.ones(x >> k)
	w.put(0, 1)
	w.put(x, k)
}

func (w *bitWriter) bytes() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.acc<<(8-w.n)))
		w.n = 0
	}
	return w.buf
}

type bitReader struct {
	b   []byte
	pos uint // bit position
	bad bool
}

func (r *bitReader) bit() uint64 {
	i := r.pos >> 3
	if int(i) >= len(r.b) {
		r.bad = true
		return 0
	}
	v := uint64(r.b[i]>>(7-r.pos&7)) & 1
	r.pos++
	return v
}

func (r *bitReader) get(nb uint) uint64 {
	var v uint64
	for ; nb > 0; nb-- {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *bitReader) gamma() uint64 {
	z := uint(0)
	for r.bit() == 0 {
		if r.bad || z > 40 {
			r.bad = true
			return 1
		}
		z++
	}
	return 1<<z | r.get(z)
}

func (r *bitReader) rice(k uint) uint64 {
	var q uint64
	for r.bit() == 1 {
		if r.bad {
			return 0
		}
		q++
	}
	return q<<k | r.get(k)
}

// ---- cost model (used to pick parameters and layout) ------------------

func gammaBits(x uint64) int { return 2*bits.Len64(x) - 1 }
func riceBits(x uint64, k uint) int {
	return int(x>>k) + 1 + int(k)
}

func countBits(c uint64, kc uint) int {
	if kc == gammaCounts {
		return gammaBits(c)
	}
	return riceBits(c-1, kc)
}

func bestKG(gaps []uint64) uint {
	best, bestCost := uint(0), -1
	for k := uint(0); k <= maxKG; k++ {
		cost := 0
		for _, g := range gaps {
			cost += riceBits(g, k)
		}
		if bestCost < 0 || cost < bestCost {
			best, bestCost = k, cost
		}
	}
	return best
}

func bestKC(counts []uint64) uint {
	best, bestCost := uint(0), -1
	for k := uint(0); k <= gammaCounts; k++ {
		cost := 0
		for _, c := range counts {
			cost += countBits(c, k)
		}
		if bestCost < 0 || cost < bestCost {
			best, bestCost = k, cost
		}
	}
	return best
}

func putCount(w *bitWriter, c uint64, kc uint) {
	if kc == gammaCounts {
		w.gamma(c)
	} else {
		w.rice(c-1, kc)
	}
}

func getCount(r *bitReader, kc uint) uint64 {
	if kc == gammaCounts {
		return r.gamma()
	}
	return r.rice(kc) + 1
}

// ---- encode -----------------------------------------------------------

// Encode packs entries, which must be sorted by ascending ID with no
// duplicates, each having at least one non-zero month. An empty list
// encodes to an empty blob.
func Encode(entries []Entry) []byte {
	if len(entries) == 0 {
		return nil
	}
	a, b := encodeMonthMajor(entries), encodeSpeciesMajor(entries)
	if len(a) <= len(b) {
		return a
	}
	return b
}

func encodeMonthMajor(entries []Entry) []byte {
	var gaps, counts []uint64
	var mask uint64
	for m := 0; m < 12; m++ {
		prev := -1
		for _, e := range entries {
			if e.Months[m] == 0 {
				continue
			}
			mask |= 1 << m
			gaps = append(gaps, uint64(int(e.ID)-prev-1))
			prev = int(e.ID)
			counts = append(counts, uint64(e.Months[m]))
		}
	}
	kg, kc := bestKG(gaps), bestKC(counts)
	w := &bitWriter{buf: make([]byte, 0, 64)}
	w.put(0, 1)
	w.put(uint64(kg), 4)
	w.put(uint64(kc), 3)
	w.put(mask, 12)
	for m := 0; m < 12; m++ {
		if mask>>m&1 == 0 {
			continue
		}
		n := 0
		for _, e := range entries {
			if e.Months[m] != 0 {
				n++
			}
		}
		w.gamma(uint64(n))
		prev := -1
		for _, e := range entries {
			if e.Months[m] == 0 {
				continue
			}
			w.rice(uint64(int(e.ID)-prev-1), kg)
			prev = int(e.ID)
			putCount(w, uint64(e.Months[m]), kc)
		}
	}
	return w.bytes()
}

func encodeSpeciesMajor(entries []Entry) []byte {
	gaps := make([]uint64, 0, len(entries))
	var counts []uint64
	prev := -1
	for _, e := range entries {
		gaps = append(gaps, uint64(int(e.ID)-prev-1))
		prev = int(e.ID)
		for _, c := range e.Months {
			if c != 0 {
				counts = append(counts, uint64(c))
			}
		}
	}
	kg, kc := bestKG(gaps), bestKC(counts)
	w := &bitWriter{buf: make([]byte, 0, 64)}
	w.put(1, 1)
	w.put(uint64(kg), 4)
	w.put(uint64(kc), 3)
	w.gamma(uint64(len(entries)))
	prev = -1
	for _, e := range entries {
		w.rice(uint64(int(e.ID)-prev-1), kg)
		prev = int(e.ID)
		present, only := 0, 0
		var mask uint64
		for m, c := range e.Months {
			if c != 0 {
				present++
				only = m
				mask |= 1 << m
			}
		}
		if present == 1 {
			w.put(1, 1)
			w.put(uint64(only), 4)
			putCount(w, uint64(e.Months[only]), kc)
			continue
		}
		w.put(0, 1)
		w.put(mask, 12)
		for _, c := range e.Months {
			if c != 0 {
				putCount(w, uint64(c), kc)
			}
		}
	}
	return w.bytes()
}

// ---- decode -----------------------------------------------------------

// Decode unpacks a blob into entries sorted by ascending ID.
func Decode(blob []byte) ([]Entry, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	r := &bitReader{b: blob}
	layout := r.get(1)
	kg, kc := uint(r.get(4)), uint(r.get(3))
	if kg > maxKG {
		return nil, errCorrupt
	}
	var out []Entry
	if layout == 1 {
		n := r.gamma()
		if n > 1<<16 {
			return nil, errCorrupt
		}
		out = make([]Entry, 0, n)
		prev := -1
		for i := uint64(0); i < n && !r.bad; i++ {
			id := prev + 1 + int(r.rice(kg))
			if id > 0xFFFF {
				return nil, errCorrupt
			}
			prev = id
			e := Entry{ID: uint16(id)}
			if r.get(1) == 1 {
				m := r.get(4)
				if m >= 12 {
					return nil, errCorrupt
				}
				e.Months[m] = uint32(getCount(r, kc))
			} else {
				mask := r.get(12)
				for m := 0; m < 12; m++ {
					if mask>>m&1 == 1 {
						e.Months[m] = uint32(getCount(r, kc))
					}
				}
			}
			out = append(out, e)
		}
	} else {
		mask := r.get(12)
		byID := map[uint16]*Entry{}
		for m := 0; m < 12 && !r.bad; m++ {
			if mask>>m&1 == 0 {
				continue
			}
			n := r.gamma()
			if n > 1<<16 {
				return nil, errCorrupt
			}
			prev := -1
			for i := uint64(0); i < n && !r.bad; i++ {
				id := prev + 1 + int(r.rice(kg))
				if id > 0xFFFF {
					return nil, errCorrupt
				}
				prev = id
				e := byID[uint16(id)]
				if e == nil {
					e = &Entry{ID: uint16(id)}
					byID[uint16(id)] = e
				}
				e.Months[m] = uint32(getCount(r, kc))
			}
		}
		out = make([]Entry, 0, len(byID))
		for _, e := range byID {
			out = append(out, *e)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	}
	if r.bad {
		return nil, errCorrupt
	}
	return out, nil
}
