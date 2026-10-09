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

// Command archv2-gate measures pipeline throughput for the arch-v2 graduation
// gate (docs/architecture-decision-records/20261006-archv2-graduation-gate.md)
// by counting records at the sink, never by reading engine metrics.
//
// Each run starts one Conduit container on a shape's pipeline.yml, waits for
// the sink files to appear, discards a warmup, then takes the sink files'
// byte sizes at the start and end of a fixed window. After the container is
// removed, it counts the newline-terminated records written inside that byte
// range. The builtin file destination writes one JSON record plus '\n' per
// record, so that count is the number of records delivered in the window,
// whichever engine produced them.
//
// Runs are single and alternated (see plan), and every session carries an
// A/A control. See README.md for usage and how to read the output.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type engine string

const (
	engineV1 engine = "v1"
	engineV2 engine = "v2"
)

// Arm labels of an A/B session. A/A sessions label their replicates
// "<engine>-a" and "<engine>-b".
const (
	armV1A = "v1-a"
	armV1B = "v1-b"
	armV2  = "v2"
)

// unknown stands in for an environment detail that could not be read.
const unknown = "unknown"

type session string

const (
	sessionAAv1 session = "aa-v1"
	sessionAAv2 session = "aa-v2"
	sessionAB   session = "ab"
)

// runSpec is one single run in a session's plan.
type runSpec struct {
	Round  int
	Arm    string
	Engine engine
}

// plan returns the run order for a session.
//
// A/A sessions run two replicates of one engine per round and swap which goes
// first every other round (a-b, b-a, ...), so steady drift over a session -
// a laptop warming up, say - lands on both replicates instead of one.
//
// An A/B session runs v1, v2, v1 each round. The two v1 runs bracket the v2
// run, which cancels linear drift in the per-round A/B delta, and the pair is
// also the same-session A/A control that the gate requires beside every A/B
// result.
func plan(s session, rounds int) []runSpec {
	var out []runSpec
	for r := 1; r <= rounds; r++ {
		switch s {
		case sessionAAv1, sessionAAv2:
			e := engineV1
			if s == sessionAAv2 {
				e = engineV2
			}
			a := runSpec{Round: r, Arm: string(e) + "-a", Engine: e}
			b := runSpec{Round: r, Arm: string(e) + "-b", Engine: e}
			if r%2 == 0 {
				a, b = b, a
			}
			out = append(out, a, b)
		case sessionAB:
			out = append(out,
				runSpec{Round: r, Arm: armV1A, Engine: engineV1},
				runSpec{Round: r, Arm: armV2, Engine: engineV2},
				runSpec{Round: r, Arm: armV1B, Engine: engineV1},
			)
		}
	}
	return out
}

var sinkPathRe = regexp.MustCompile(`(?m)^\s*path:\s*"?(/sink/[^"\s]+)"?\s*$`)

// sinkPaths extracts the in-container paths of a shape's file sinks from its
// pipeline.yml. Every destination in a shape writes under /sink.
func sinkPaths(pipelineYAML []byte) []string {
	var out []string
	for _, m := range sinkPathRe.FindAllSubmatch(pipelineYAML, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

type config struct {
	Image     string
	Shape     string
	Session   session
	Rounds    int
	Warmup    time.Duration
	Window    time.Duration
	CPUs      string
	ShapesDir string
	OutDir    string

	pipelineFile string
	sinks        []string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "archv2-gate:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	d := docker{image: cfg.Image}
	if err := d.preflight(ctx); err != nil {
		return err
	}
	defer d.cleanup()

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return cerrors.Errorf("create output dir: %w", err)
	}
	env := captureEnvironment(ctx, cfg, d)
	if err := writeJSON(filepath.Join(cfg.OutDir, "env.json"), env); err != nil {
		return err
	}

	specs := plan(cfg.Session, cfg.Rounds)
	var results []runResult
	for i, spec := range specs {
		fmt.Fprintf(os.Stderr, "[%d/%d] round %d arm %s (%s) ...\n", i+1, len(specs), spec.Round, spec.Arm, spec.Engine)
		res, err := d.measure(ctx, cfg, spec)
		if err != nil {
			return cerrors.Errorf("round %d arm %s: %w", spec.Round, spec.Arm, err)
		}
		fmt.Fprintf(os.Stderr, "      %.0f records/s per sink (%d records over %.1fs, vm load %s)\n",
			res.Rate, sum(res.SinkRecords), res.WindowSec, res.VMLoadAfter)
		results = append(results, res)
		// Written after every run so an interrupted session keeps what it has.
		if err := writeRunsCSV(filepath.Join(cfg.OutDir, "runs.csv"), results); err != nil {
			return err
		}
	}

	env.Finished = time.Now().UTC()
	if err := writeJSON(filepath.Join(cfg.OutDir, "env.json"), env); err != nil {
		return err
	}

	summary := renderSummary(cfg, results)
	fmt.Print(summary)
	return os.WriteFile(filepath.Join(cfg.OutDir, "summary.md"), []byte(summary), 0o600)
}

func parseFlags() (config, error) {
	var cfg config
	var sess string
	flag.StringVar(&cfg.Image, "image", "conduit-bench:archv2-gate", "Conduit image to run; build it from the working tree (see README.md)")
	flag.StringVar(&cfg.Shape, "shape", "1x1", "pipeline shape: a directory under -shapes-dir holding pipeline.yml (1x1, 2x2, 2x2-batched)")
	flag.StringVar(&sess, "session", string(sessionAAv1), "aa-v1, aa-v2 (A/A control of one engine) or ab (v1-v2-v1 per round)")
	flag.IntVar(&cfg.Rounds, "rounds", 5, "rounds per session")
	flag.DurationVar(&cfg.Warmup, "warmup", 20*time.Second, "time discarded after the sinks start growing")
	flag.DurationVar(&cfg.Window, "window", 60*time.Second, "measurement window per run")
	flag.StringVar(&cfg.CPUs, "cpus", "", "optional docker --cpus limit for the Conduit container")
	flag.StringVar(&cfg.ShapesDir, "shapes-dir", "benchi/archv2-gate/shapes", "directory holding the shapes")
	flag.StringVar(&cfg.OutDir, "out", "", "output directory (default benchi/archv2-gate/results/<UTC time>-<shape>-<session>)")
	flag.Parse()

	cfg.Session = session(sess)
	switch cfg.Session {
	case sessionAAv1, sessionAAv2, sessionAB:
	default:
		return cfg, cerrors.Errorf("unknown -session %q (want aa-v1, aa-v2 or ab)", sess)
	}
	if cfg.Rounds < 2 {
		return cfg, cerrors.Errorf("-rounds must be at least 2, got %d: one round has no spread to report", cfg.Rounds)
	}
	if cfg.Window < 60*time.Second {
		fmt.Fprintf(os.Stderr, "warning: -window %s is below the 60s the gate requires; at 30s the A/A floor was about ±13%% (benchi/METHODOLOGY.md)\n", cfg.Window)
	}

	shapeDir, err := filepath.Abs(filepath.Join(cfg.ShapesDir, cfg.Shape))
	if err != nil {
		return cfg, cerrors.Errorf("resolve shape dir: %w", err)
	}
	cfg.pipelineFile = filepath.Join(shapeDir, "pipeline.yml")
	raw, err := os.ReadFile(cfg.pipelineFile)
	if err != nil {
		return cfg, cerrors.Errorf("read shape %q: %w", cfg.Shape, err)
	}
	cfg.sinks = sinkPaths(raw)
	if len(cfg.sinks) == 0 {
		return cfg, cerrors.Errorf("shape %q has no destination writing under /sink", cfg.Shape)
	}

	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join("benchi/archv2-gate/results",
			fmt.Sprintf("%s-%s-%s", time.Now().UTC().Format("20060102T150405Z"), cfg.Shape, cfg.Session))
	}
	return cfg, nil
}

func sum(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}
