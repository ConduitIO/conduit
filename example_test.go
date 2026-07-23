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

package conduit_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	conduit "github.com/conduitio/conduit"
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
)

// helloPipeline runs the full New->Run->Import->Stop lifecycle with a single
// generator->log pipeline, entirely in-memory. It is the shared logic behind
// Example_helloPipeline (compiled+run by `go test`, checked against captured
// Output) and TestExampleHelloPipeline_WithinBudget (a CI-timed wall-clock
// budget assertion — deliberately separate from the human-facing "15 minutes
// to a running pipeline" doc claim, which is a UX statement, not something a
// test can measure; see the embed workstream plan §12 open question 1).
//
// New below does not open anything (B1 DX-hardening fix: the database opens
// lazily, on this very Run call — see New's and Engine's "Lifecycle contract"
// doc); Run's own success-then-Stop path already releases it via Runtime's
// cleanup, which is why this example never calls Engine.Close explicitly —
// see Close's doc for when it would still be needed (e.g. if Run below had
// failed, or if the engine were discarded before ever being run).
func helloPipeline(ctx context.Context, dir string) error {
	e, err := conduit.New(ctx, conduit.Options{
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		DB:           conduit.DBOptions{Type: "inmemory"},
		PipelinesDir: dir,
	})
	if err != nil {
		return err
	}

	h, err := e.Run(ctx)
	if err != nil {
		return err
	}

	err = e.Import(ctx, conduit.PipelineConfig{
		ID:   "hello-pipeline",
		Name: "hello-pipeline",
		Connectors: []provisioningconfig.Connector{
			{
				ID:     "src",
				Type:   "source",
				Plugin: "builtin:generator",
				Settings: map[string]string{
					"format.type": "raw",
					"recordCount": "5",
				},
			},
			{
				ID:     "dst",
				Type:   "destination",
				Plugin: "builtin:log",
			},
		},
	})
	if err != nil {
		_ = h.Stop(ctx)
		return err
	}

	if err := e.StartPipeline(ctx, "hello-pipeline"); err != nil {
		_ = h.Stop(ctx)
		return err
	}

	// Let a handful of generated records flow through to the log destination.
	time.Sleep(50 * time.Millisecond)

	// Invariant 7: Stop alone drains every running pipeline (including this
	// one) and checkpoints before returning — no separate per-pipeline stop
	// call is needed first.
	return h.Stop(ctx)
}

// Example_helloPipeline demonstrates the three-call embed lifecycle
// (New -> Run -> Stop) plus Import, end to end: an in-memory database, a
// generator source, and a log destination. This is the literal example a
// `go get github.com/conduitio/conduit` embedder copies first.
func Example_helloPipeline() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "conduit-embed-example-*")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer os.RemoveAll(dir)

	if err := helloPipeline(ctx, dir); err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("pipeline stopped cleanly")
	// Output: pipeline stopped cleanly
}

// TestExampleHelloPipeline_WithinBudget is the CI-timed half of AC-8: it
// asserts the doc example's own execution — construct, run, import, start,
// stop — stays within a generous wall-clock ceiling. This is deliberately
// NOT a claim about how long a human takes to copy-paste the doc and get a
// pipeline running (that's a UX/docs-quality metric, not something a test
// process's clock can measure) — the plan's §12 open question 1 flags this
// distinction explicitly and recommends keeping the two separate rather than
// conflating them. The ceiling here (60s) is generous on purpose: it exists
// to catch a hang/regression (e.g. a Stop that no longer returns), not to
// benchmark performance.
func TestExampleHelloPipeline_WithinBudget(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()

	if err := helloPipeline(context.Background(), dir); err != nil {
		t.Fatalf("helloPipeline failed: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 60*time.Second {
		t.Fatalf("helloPipeline took %s, exceeding the 60s CI wall-clock budget", elapsed)
	}
}
