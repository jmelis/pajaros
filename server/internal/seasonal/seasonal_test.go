package seasonal

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func randEntries(rng *rand.Rand) []Entry {
	n := 1 + rng.Intn(60)
	if rng.Intn(10) == 0 {
		n = 1 + rng.Intn(1500)
	}
	ids := map[uint16]bool{}
	for len(ids) < n {
		switch rng.Intn(4) {
		case 0:
			ids[uint16(rng.Intn(64))] = true
		case 1:
			ids[uint16(11160+rng.Intn(10))] = true
		default:
			ids[uint16(rng.Intn(65536))] = true
		}
	}
	var out []Entry
	for id := range ids {
		e := Entry{ID: id}
		pick := func() uint32 {
			switch rng.Intn(10) {
			case 0:
				return uint32(1 + rng.Intn(1<<28))
			case 1, 2:
				return uint32(1 + rng.Intn(2000))
			default:
				return uint32(1 + rng.Intn(4))
			}
		}
		switch rng.Intn(3) {
		case 0:
			e.Months[rng.Intn(12)] = pick()
		case 1:
			for m := 0; m < 12; m++ {
				e.Months[m] = pick()
			}
		default:
			for k := 0; k < 1+rng.Intn(5); k++ {
				e.Months[rng.Intn(12)] = pick()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		in := randEntries(rng)
		got, err := Decode(Encode(in))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("case %d: round trip mismatch\n in: %v\ngot: %v", i, in, got)
		}
	}
}

func TestEdgeCases(t *testing.T) {
	cases := [][]Entry{
		{{ID: 0, Months: Months{1}}},
		{{ID: 65535, Months: Months{11: 4000000000}}},
		{{ID: 5, Months: Months{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}}},
		{{ID: 0, Months: Months{3: 1}}, {ID: 65535, Months: Months{3: 2}}},
	}
	for i, in := range cases {
		got, err := Decode(Encode(in))
		if err != nil || !reflect.DeepEqual(got, in) {
			t.Errorf("case %d: got %v err %v want %v", i, got, err, in)
		}
	}
	if b := Encode(nil); len(b) != 0 {
		t.Errorf("empty list should encode to an empty blob, got %d bytes", len(b))
	}
	if got, err := Decode(nil); got != nil || err != nil {
		t.Errorf("empty blob should decode to nothing, got %v %v", got, err)
	}
}

func TestTruncatedBlobIsAnError(t *testing.T) {
	in := []Entry{{ID: 10, Months: Months{5, 0, 7}}, {ID: 900, Months: Months{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}}
	b := Encode(in)
	for n := 1; n < len(b); n++ {
		if got, err := Decode(b[:n]); err == nil && reflect.DeepEqual(got, in) {
			t.Fatalf("truncation to %d bytes decoded as the original", n)
		}
	}
}

func TestTotal(t *testing.T) {
	m := Months{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if m.Total() != 78 {
		t.Errorf("Total = %d, want 78", m.Total())
	}
}
