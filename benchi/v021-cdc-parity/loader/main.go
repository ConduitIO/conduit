// Command loader is the workload and measurement tool of the CDC parity kit.
// It is ONE static binary, mounted into the same alpine containers for every
// arm, so the workload and the ground truth are identical by construction;
// only the pipeline under test differs.
//
//	loader [load] -url ...      Postgres workload (the default, unchanged flags)
//	loader fill-kafka ...       pre-fill a Kafka topic for the Kafka-source cells
//	loader fill-file ...        pre-fill a file for the file-source cells
//	loader receive ...          HTTP receiver: the ground truth of HTTP sinks
//	loader sample ...           time series of source progress, sink counts, latency
//	loader verify ...           record-by-record check of a sink against the workload
//	loader now|first|wait-file|mkbucket   helpers the benchi hooks call
//
// # load
//
// Writes a mix of INSERT/UPDATE/DELETE against bench_source_0 ..
// bench_source_{N-1}. Per table it keeps a sliding window of live rows
// [low, high) and cycles:
//   - INSERT (default 70%): a new row with the next id
//   - UPDATE (default 20%): rewrite the payload of a random live row
//   - DELETE (default 10%): remove the oldest live row (log-retention style)
//
// With -tables N the SAME total rate is split round-robin across the tables:
// the workload is split, not multiplied, so raising N varies only the
// pipeline's parallelism, never the offered load.
//
// The op sequence is deterministic (gen.go): it depends only on -tables,
// -window, -batch, the op fractions and -seed, never on time or rate. Every
// inserted or updated payload carries a marker naming the change it came from
// and the time it was queued. `verify` replays the same sequence to check a
// sink record by record (loss, duplicates, reordering), and `sample` reads the
// timestamps to measure end-to-end latency.
//
// Rate: -rate ops/s, or -rate 0 for as fast as Postgres takes them (the
// capacity cells), or -schedule "20000:210s,40000:210s,..." for an offered-rate
// ramp (each step's start is logged as STEP lines). -total stops the workload
// after that many ops (the correctness and recovery runs, which need a known
// end); the loader then idles so its container stays up.
//
// Statements go through pgx.Batch, one round trip per -batch statements, over
// -conns connections (tables are assigned to connections by table index, so
// per-table order is the generated order). The loader's own progress
// (committed ops by kind) is served on -http, which `sample` reads: that is the
// offered rate the loader ACHIEVED, recorded next to every sink count.
//
// -slots waits for the named replication slots before writing anything, fill
// included: a snapshotMode=never source must exist before the first change, or
// the change is never captured. -window rows per table are inserted first (the
// initial fill, part of the replayed sequence); -fill-only inserts the window
// and stops, which the snapshot cells use to pre-load the table before the
// pipeline starts.
package main

import (
	"fmt"
	"os"
)

func main() {
	cmd := "load"
	args := os.Args[1:]
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "load":
		err = runLoad(args)
	case "fill-kafka":
		err = runFillKafka(args)
	case "fill-file":
		err = runFillFile(args)
	case "receive":
		err = runReceive(args)
	case "sample":
		err = runSample(args)
	case "verify":
		err = runVerify(args)
	case "now":
		err = runNow(args)
	case "first":
		err = runFirst(args)
	case "wait-file":
		err = runWaitFile(args)
	case "mkbucket":
		err = runMkBucket(args)
	default:
		err = fmt.Errorf("unknown command %q (load, fill-kafka, fill-file, receive, sample, verify, now, first, wait-file, mkbucket)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loader:", err)
		os.Exit(1)
	}
}
