package areas

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/jmelis/pajaros/server/internal/seasonal"

	bolt "go.etcd.io/bbolt"
)

// flushBytes is how much pending data a Builder accumulates before it commits
// a bbolt transaction.
const flushBytes = 256 << 20

type kv struct{ k, v []byte }

// Builder writes an seasonal_cells.bolt file. Points must be added in non-decreasing
// order of their 0.01-degree cell (latitude index, then longitude index) so
// that every Put lands in ascending key order, bbolt's append fast path, and
// so that a one-degree latitude band is complete once the first point of the
// next band arrives.
type Builder struct {
	db      *bolt.DB
	pending map[string][]kv
	size    int

	curI, curJ int32
	curCell    []byte
	started    bool

	band   int32
	l0     map[[2]int32]accumulator
	points int
	Totals [4]uint64 // records seen: input points, then cell levels 0..2 as written
}

// NewBuilder creates (replacing) the file at path.
func NewBuilder(path string) (*Builder, error) {
	os.Remove(path)
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, n := range append([]string{bucketPoints, bucketMeta}, levelBucket[:]...) {
			if _, err := tx.CreateBucket([]byte(n)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Builder{db: db, pending: map[string][]kv{}, l0: map[[2]int32]accumulator{}}, nil
}

func (b *Builder) put(bucket string, k, v []byte) error {
	b.pending[bucket] = append(b.pending[bucket], kv{k, v})
	b.size += len(k) + len(v)
	if b.size >= flushBytes {
		return b.commit()
	}
	return nil
}

func (b *Builder) commit() error {
	err := b.db.Update(func(tx *bolt.Tx) error {
		for name, list := range b.pending {
			bk := tx.Bucket([]byte(name))
			bk.FillPercent = 1.0
			for _, e := range list {
				if err := bk.Put(e.k, e.v); err != nil {
					return err
				}
			}
		}
		return nil
	})
	b.pending = map[string][]kv{}
	b.size = 0
	return err
}

// Add records one point and its species blob (the seasonal encoding).
func (b *Builder) Add(lat, lng float64, blob []byte) error {
	i, j := idx(lat), idx(lng)
	if b.started && (i < b.curI || (i == b.curI && j < b.curJ)) {
		return fmt.Errorf("areas: point (%v,%v) out of order", lat, lng)
	}
	if !b.started || i != b.curI || j != b.curJ {
		if err := b.endCell(); err != nil {
			return err
		}
		if bd := floorDiv(i, levelSteps[2]); b.started && bd != b.band {
			if err := b.flushBand(); err != nil {
				return err
			}
		}
		b.band = floorDiv(i, levelSteps[2])
		b.curI, b.curJ, b.started = i, j, true
	}
	es, err := seasonal.Decode(blob)
	if err != nil {
		return err
	}
	k := [2]int32{floorDiv(i, levelSteps[0]), floorDiv(j, levelSteps[0])}
	acc := b.l0[k]
	if acc == nil {
		acc = accumulator{}
		b.l0[k] = acc
	}
	acc.add(es)
	for _, e := range es {
		b.Totals[0] += uint64(e.Months.Total())
	}
	b.curCell = appendPoint(b.curCell, lat, lng, i, j, blob)
	b.points++
	return nil
}

func (b *Builder) endCell() error {
	if !b.started || len(b.curCell) == 0 {
		return nil
	}
	v := b.curCell
	b.curCell = nil
	return b.put(bucketPoints, cellKey(b.curI, b.curJ), v)
}

func sortedKeys(m map[[2]int32]accumulator) [][2]int32 {
	ks := make([][2]int32, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(a, c int) bool {
		if ks[a][0] != ks[c][0] {
			return ks[a][0] < ks[c][0]
		}
		return ks[a][1] < ks[c][1]
	})
	return ks
}

// flushBand writes the finished one-degree latitude band at the three cell
// levels, deriving each level from the one below it.
func (b *Builder) flushBand() error {
	cur := b.l0
	b.l0 = map[[2]int32]accumulator{}
	for level := range 3 {
		for _, k := range sortedKeys(cur) {
			es := cur[k].entries()
			for _, e := range es {
				b.Totals[level+1] += uint64(e.Months.Total())
			}
			if err := b.put(levelBucket[level], cellKey(k[0], k[1]), seasonal.Encode(es)); err != nil {
				return err
			}
		}
		if level == 2 {
			break
		}
		ratio := levelSteps[level+1] / levelSteps[level]
		next := map[[2]int32]accumulator{}
		for k, acc := range cur {
			nk := [2]int32{floorDiv(k[0], ratio), floorDiv(k[1], ratio)}
			na := next[nk]
			if na == nil {
				na = accumulator{}
				next[nk] = na
			}
			na.add(acc.entries())
		}
		cur = next
	}
	return nil
}

// Finish flushes everything, writes the metadata and closes the file.
func (b *Builder) Finish() error {
	if err := b.endCell(); err != nil {
		return err
	}
	if b.started {
		if err := b.flushBand(); err != nil {
			return err
		}
	}
	if err := b.commit(); err != nil {
		return err
	}
	err := b.db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket([]byte(bucketMeta))
		if err := m.Put([]byte(metaFormat), []byte(FormatValue)); err != nil {
			return err
		}
		n := make([]byte, 8)
		binary.BigEndian.PutUint64(n, uint64(b.points))
		return m.Put([]byte(metaPoints), n)
	})
	if err != nil {
		b.db.Close()
		return err
	}
	return b.db.Close()
}
