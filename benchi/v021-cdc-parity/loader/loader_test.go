package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func testGen() genConfig {
	return genConfig{Tables: 3, Window: 50, Batch: 7, InsertFrac: 0.7, UpdateFrac: 0.2, Seed: 42}
}

// The verifier's whole premise: the sequence depends on the op count alone,
// not on how many ops each batch pulled (the -total tail, -conns grouping).
func TestGeneratorIsAFunctionOfOpCount(t *testing.T) {
	a, b := newGenerator(testGen()), newGenerator(testGen())
	for a.filling() {
		a.next()
		b.next()
	}
	for i := 0; i < 5000; i++ {
		if x, y := a.next(), b.next(); x != y {
			t.Fatalf("op %d differs: %+v vs %+v", i, x, y)
		}
	}
}

func TestGeneratorInvariants(t *testing.T) {
	g := newGenerator(testGen())
	live := make([]map[int64]bool, 3)
	seq := make([]int64, 3)
	for i := range live {
		live[i] = map[int64]bool{}
	}
	for i := 0; i < 20000; i++ {
		o := g.next()
		if o.Seq != seq[o.Table] {
			t.Fatalf("table %d: seq %d, want %d", o.Table, o.Seq, seq[o.Table])
		}
		seq[o.Table]++
		switch o.Kind {
		case opInsert:
			if live[o.Table][o.ID] {
				t.Fatalf("insert of live id %d", o.ID)
			}
			live[o.Table][o.ID] = true
		case opUpdate, opDelete:
			if !live[o.Table][o.ID] {
				t.Fatalf("%s of dead id %d", o.Kind, o.ID)
			}
			if o.Kind == opDelete {
				delete(live[o.Table], o.ID)
			}
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	now := time.Unix(1793000000, 123456789)
	for _, size := range []int{48, 64, 1024, 16384} {
		o := op{Kind: opUpdate, Table: 12, ID: 99, Seq: 1234567}
		p := encodePayload(o, size, now)
		if len(p) != size {
			t.Fatalf("size %d: payload is %d bytes", size, len(p))
		}
		// Embedded the way each sink writes it.
		for _, line := range []string{
			p,
			`{"before":null,"after":{"id":99,"payload":"` + p + `"},"op":"u"}`,
			`{"schema":{},"payload":{"id":99,"payload":"` + p + `"}}`,
			`{"key":{"id":99},"payload":{"after":{"payload":"` + p + `"}}}`,
		} {
			ms := findMarkers(line)
			if len(ms) != 1 {
				t.Fatalf("size %d: %d markers in %.60q", size, len(ms), line)
			}
			if m := ms[0]; m.Table != 12 || m.Seq != 1234567 || m.TS != now.UnixNano() || m.Len != size {
				t.Fatalf("size %d: decoded %+v", size, m)
			}
		}
	}
	if ms := findMarkers(`{"id":5,"msg":"xS1.2|T123|"}`); len(ms) != 0 {
		t.Fatalf("false marker: %+v", ms)
	}
}

// sinkLines renders the replayed sequence the way a Debezium file sink writes
// it: one line per change, deletes as a before image with only the key.
func sinkLines(ex *expected, payload int) [][]string {
	out := make([][]string, ex.tables)
	for tb, ops := range ex.ops {
		for _, o := range ops {
			var l string
			if o.Kind == opDelete {
				l = fmt.Sprintf(`{"before":{"id":%d},"after":null,"source":{"table":"bench_source_%d"},"op":"d"}`, o.ID, tb)
			} else {
				l = fmt.Sprintf(`{"before":null,"after":{"id":%d,"payload":"%s"},"op":"c"}`, o.ID, encodePayload(o, payload, time.Now()))
			}
			out[tb] = append(out[tb], l)
		}
	}
	return out
}

func feed(v *eventVerifier, lines [][]string) {
	for tb, ls := range lines {
		for _, l := range ls {
			v.line(fmt.Sprintf("/sink/out_%d.txt", tb), l)
		}
	}
}

func TestVerifyCleanDelivery(t *testing.T) {
	ex := replay(testGen(), 3000)
	v := newEventVerifier(ex, 64, true, false)
	feed(v, sinkLines(ex, 64))
	rep := v.report()
	if rep["ok"] != true {
		t.Fatalf("clean delivery not ok: %+v", rep)
	}
}

func TestVerifyFindsLossDuplicatesReorderCorruption(t *testing.T) {
	ex := replay(testGen(), 3000)
	lines := sinkLines(ex, 64)
	// table 0: drop line 10 (loss), repeat line 20 at the end (redelivery,
	// a duplicate and not a reorder), swap 30/31 (reorder)
	l0 := lines[0]
	dropped := l0[10]
	l0 = append(append([]string{}, l0[:10]...), l0[11:]...)
	l0 = append(l0, l0[19])
	l0[29], l0[30] = l0[30], l0[29]
	lines[0] = l0
	// table 1: corrupt the payload length of one insert
	for i, l := range lines[1] {
		if strings.Contains(l, `"op":"c"`) {
			lines[1][i] = strings.Replace(l, "xxxx", "xxx", 1)
			break
		}
	}
	v := newEventVerifier(ex, 64, true, false)
	feed(v, lines)
	rep := v.report()
	if rep["ok"] != false {
		t.Fatal("damaged delivery reported ok")
	}
	if got := rep["missing"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("missing = %d, want 1 (%s)", got, dropped)
	}
	if got := rep["duplicates"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("duplicates = %d, want 1", got)
	}
	if got := rep["reordered"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("reordered = %d, want 1", got)
	}
	if got := rep["malformed"].(int64); got != 1 {
		t.Fatalf("malformed = %d, want 1", got)
	}
}

func TestScheduleParse(t *testing.T) {
	s, err := parseSchedule("20000:210s, 40000:3m30s,0:5m")
	if err != nil || len(s) != 3 || s[1].rate != 40000 || s[1].dur != 210*time.Second || s[2].rate != 0 {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := parseSchedule("20000"); err == nil {
		t.Fatal("missing duration accepted")
	}
}
