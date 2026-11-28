// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestWriterShareCoversWindowExactly is the partitioning invariant the whole
// multi-writer design rests on: for ANY number of writers, the union of the
// writers' initial ids is exactly [0, window), with no gap and no overlap. If
// this ever fails, the table the CDC arms capture depends on -writers, and no
// result is comparable to any other.
func TestWriterShareCoversWindowExactly(t *testing.T) {
	t.Parallel()
	for _, win := range []int{1, 2, 3, 7, 10, 64, 1000, 10000} {
		for _, n := range []int{1, 2, 3, 4, 7, 8, 16} {
			if win < n {
				continue // rejected at startup: every writer needs >= 1 row
			}
			seen := make(map[int64]int, win)
			var total int64
			for _, w := range newWriters(n, 1, win) {
				share := w.wins[0].high
				total += share
				for slot := int64(0); slot < share; slot++ {
					seen[w.id(slot)]++
				}
			}
			if total != int64(win) {
				t.Fatalf("win=%d n=%d: shares sum to %d, want %d", win, n, total, win)
			}
			if len(seen) != win {
				t.Fatalf("win=%d n=%d: %d distinct ids, want %d", win, n, len(seen), win)
			}
			for id := int64(0); id < int64(win); id++ {
				if seen[id] != 1 {
					t.Fatalf("win=%d n=%d: id %d covered %d times, want exactly 1", win, n, id, seen[id])
				}
			}
		}
	}
}

// TestWriterRangesStayDisjoint asserts that the disjointness survives steady
// state, not just the fill: after many operations, no id a writer touched is
// an id another writer touched. This is what makes UPDATE and DELETE targeting
// deterministic — a writer only ever rewrites or removes a row it inserted.
func TestWriterRangesStayDisjoint(t *testing.T) {
	t.Parallel()
	const (
		n      = 8
		tables = 3
		win    = 800
		ops    = 20000
	)
	owner := make(map[int64]int) // id -> writer index, per table (offset below)
	ws := newWriters(n, tables, win)
	for w := range ws {
		// Fill-time ids count too: a writer owns them from the start.
		for tbl := 0; tbl < tables; tbl++ {
			for slot := int64(0); slot < ws[w].wins[tbl].high; slot++ {
				key := int64(tbl)<<40 | ws[w].id(slot)
				if prev, ok := owner[key]; ok && prev != w {
					t.Fatalf("fill: table %d id %d owned by writers %d and %d", tbl, ws[w].id(slot), prev, w)
				}
				owner[key] = w
			}
		}
	}
	rngs := make([]*rand.Rand, n)
	for w := range rngs {
		rngs[w] = rand.New(rand.NewSource(baseSeed + int64(w)))
	}
	for i := 0; i < ops; i++ {
		w := i % n
		tbl := i % tables
		o := ws[w].nextOp(tbl, rngs[w], 0.70, 0.20)
		key := int64(tbl)<<40 | o.id
		if prev, ok := owner[key]; ok && prev != w {
			t.Fatalf("steady state: table %d id %d touched by writers %d and %d", tbl, o.id, prev, w)
		}
		owner[key] = w
		if o.id%int64(n) != int64(w) {
			t.Fatalf("writer %d produced id %d, which is not congruent to %d mod %d", w, o.id, w, n)
		}
	}
}

// TestSingleWriterReproducesHistoricalWorkload pins the shape the committed
// results were measured with: one writer, stride 1, ids identical to the slot
// indices, and the same RNG seed. If this fails, -writers 1 is no longer the
// pre-writers loader and NO committed number is comparable.
func TestSingleWriterReproducesHistoricalWorkload(t *testing.T) {
	t.Parallel()
	const win = 10000
	ws := newWriters(1, 1, win)
	if len(ws) != 1 {
		t.Fatalf("got %d writers, want 1", len(ws))
	}
	w := ws[0]
	if w.index != 0 || w.stride != 1 {
		t.Fatalf("index=%d stride=%d, want 0 and 1", w.index, w.stride)
	}
	if w.wins[0] != (window{low: 0, high: win}) {
		t.Fatalf("window=%+v, want {0 %d}", w.wins[0], win)
	}
	for _, slot := range []int64{0, 1, 42, win - 1, win, 1 << 20} {
		if got := w.id(slot); got != slot {
			t.Fatalf("id(%d)=%d, want the slot itself", slot, got)
		}
	}

	// The historical loader's reference implementation, inlined: a single
	// low/high window over raw ids with the same rolls off the same seed.
	ref := window{low: 0, high: win}
	refRNG := rand.New(rand.NewSource(baseSeed))
	gotRNG := rand.New(rand.NewSource(baseSeed))
	for i := 0; i < 200000; i++ {
		var want op
		roll := refRNG.Float64()
		switch {
		case roll < 0.70:
			want = op{kind: opInsert, id: ref.high}
			ref.high++
		case roll < 0.90:
			want = op{kind: opUpdate, id: ref.low + refRNG.Int63n(ref.high-ref.low)}
		default:
			if ref.high-ref.low > 1 {
				want = op{kind: opDelete, id: ref.low}
				ref.low++
			} else {
				want = op{kind: opInsert, id: ref.high}
				ref.high++
			}
		}
		if got := w.nextOp(0, gotRNG, 0.70, 0.20); got != want {
			t.Fatalf("op %d: got %+v, want %+v", i, got, want)
		}
	}
}

// TestOpMixIsUnchanged asserts the measured workload mix: 70/20/10
// INSERT/UPDATE/DELETE, whatever the writer count. Changing the mix would
// silently invalidate every committed result, so it is a test, not a comment.
func TestOpMixIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 4, 16} {
		const ops = 200000
		counts := map[opKind]int{}
		ws := newWriters(n, 1, 10000)
		for w := range ws {
			rng := rand.New(rand.NewSource(baseSeed + int64(w)))
			for i := 0; i < ops/n; i++ {
				counts[ws[w].nextOp(0, rng, 0.70, 0.20).kind]++
			}
		}
		total := float64(counts[opInsert] + counts[opUpdate] + counts[opDelete])
		for kind, want := range map[opKind]float64{opInsert: 0.70, opUpdate: 0.20, opDelete: 0.10} {
			if got := float64(counts[kind]) / total; math.Abs(got-want) > 0.01 {
				t.Errorf("writers=%d: kind %d fraction %.4f, want %.2f +/- 0.01", n, kind, got, want)
			}
		}
	}
}

// TestUpdateAndDeleteTargetLiveRows asserts UPDATE and DELETE never address a
// row the writer has already deleted or has not yet inserted: the window stays
// dense and a live row is always available. A loader that updated dead ids
// would produce no-op statements, under-generating WAL and overstating the
// offered rate.
func TestUpdateAndDeleteTargetLiveRows(t *testing.T) {
	t.Parallel()
	const n = 4
	ws := newWriters(n, 1, 100)
	for w := range ws {
		live := make(map[int64]bool)
		for slot := int64(0); slot < ws[w].wins[0].high; slot++ {
			live[ws[w].id(slot)] = true
		}
		rng := rand.New(rand.NewSource(baseSeed + int64(w)))
		for i := 0; i < 50000; i++ {
			o := ws[w].nextOp(0, rng, 0.70, 0.20)
			switch o.kind {
			case opInsert:
				if live[o.id] {
					t.Fatalf("writer %d op %d: INSERT of already-live id %d", w, i, o.id)
				}
				live[o.id] = true
			case opUpdate:
				if !live[o.id] {
					t.Fatalf("writer %d op %d: UPDATE of dead id %d", w, i, o.id)
				}
			case opDelete:
				if !live[o.id] {
					t.Fatalf("writer %d op %d: DELETE of dead id %d", w, i, o.id)
				}
				delete(live, o.id)
			}
			if len(live) == 0 {
				t.Fatalf("writer %d op %d: ran out of live rows", w, i)
			}
		}
	}
}

// TestDeleteKeepsOneLiveRow drives a one-row window hard: the delete branch
// must fall back to an INSERT rather than empty the window.
func TestDeleteKeepsOneLiveRow(t *testing.T) {
	t.Parallel()
	w := newWriters(1, 1, 1)[0]
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 10000; i++ {
		w.nextOp(0, rng, 0.0, 0.0) // all-delete mix
		if got := w.wins[0].high - w.wins[0].low; got < 1 {
			t.Fatalf("op %d: %d live rows, want >= 1", i, got)
		}
	}
}

// TestSharedBucketLimitsAggregateRate asserts the token bucket is AGGREGATE:
// W writers drawing from it together deliver the configured rate, not W times
// it. Without that, -writers would silently multiply the offered load and the
// arms would no longer see the same workload.
func TestSharedBucketLimitsAggregateRate(t *testing.T) {
	t.Parallel()
	const (
		target  = 20000
		batch   = 1000
		writers = 8
		grants  = 40 // 40 * 1000 ops at 20k ops/s = about 2s
	)
	lim := rate.NewLimiter(rate.Limit(target), batch)
	start := time.Now()
	done := make(chan struct{})
	granted := make(chan struct{}, grants)
	for w := 0; w < writers; w++ {
		go func() {
			for {
				if err := lim.WaitN(context.Background(), batch); err != nil {
					return
				}
				select {
				case granted <- struct{}{}:
				case <-done:
					return
				}
			}
		}()
	}
	for i := 0; i < grants; i++ {
		<-granted
	}
	close(done)
	elapsed := time.Since(start).Seconds()
	achieved := float64(grants*batch) / elapsed
	// The first burst is free (burst == batch), so allow generous slack on the
	// high side; the point is that it is near target, not near writers*target.
	if achieved > 1.5*target {
		t.Fatalf("aggregate rate %.0f ops/s with %d writers, want about %d (bucket is not shared?)", achieved, writers, target)
	}
}

// TestUnboundedLimiterDoesNotWait asserts -unbounded really is flat out: the
// ceiling run must not be shaped by the bucket it is meant to bypass.
func TestUnboundedLimiterDoesNotWait(t *testing.T) {
	t.Parallel()
	lim := rate.NewLimiter(rate.Inf, 1000)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		if err := lim.WaitN(context.Background(), 1000); err != nil {
			t.Fatalf("WaitN: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("1000 unbounded grants of 1000 took %s, want no waiting", elapsed)
	}
}

// TestBoundedLimiterAcceptsAFullBatch guards the burst setting: WaitN(n) fails
// outright when n exceeds the burst, which would abort every writer instantly.
func TestBoundedLimiterAcceptsAFullBatch(t *testing.T) {
	t.Parallel()
	const batch = 1000
	lim := rate.NewLimiter(rate.Limit(50000), batch)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lim.WaitN(ctx, batch); err != nil {
		t.Fatalf("WaitN(batch) with burst == batch: %v", err)
	}
}

func TestSplitSlots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"whitespace only", " , ,, ", nil},
		{"single", "s0", []string{"s0"}},
		{"multiple trimmed", " s0 , s1 ", []string{"s0", "s1"}},
		{"deduped in first-seen order", "s1,s0,s1", []string{"s1", "s0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitSlots(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestMissingSlots(t *testing.T) {
	t.Parallel()
	present := map[string]bool{"a": true, "c": true}
	got := missingSlots([]string{"a", "b", "c", "d"}, present)
	want := []string{"b", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if got := missingSlots([]string{"a", "c"}, present); got != nil {
		t.Fatalf("got %q, want nil when nothing is missing", got)
	}
}
