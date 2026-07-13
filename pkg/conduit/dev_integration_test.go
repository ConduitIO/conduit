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
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestRunDev_HotReload_EndToEnd is the design doc's PR2 integration AC (see
// docs/design-documents/20260712-pipeline-dev-hot-reload.md §4 "Testing"):
// a real generator -> processor -> file pipeline, run under `--dev` (i.e.
// Config.Dev.Enabled, exactly what cmd/conduit/root/run.RunCommand sets),
// watched by a REAL fsnotify.Watcher over a real temp directory (no fakes
// anywhere in this test — pkg/conduit/dev's own package tests cover the
// fake-driven unit cases).
//
// It exercises, in order:
//  1. Editing the processor's config -> applied in place (no restart log,
//     output reflects the new value).
//  2. Editing the source connector's setting -> applied via a labeled
//     restart (a restart log appears).
//  3. Saving a syntax error -> the pipeline keeps running (no further
//     restart, output keeps growing) and the error is logged.
//
// "No restart" is verified the way the design doc's AC literally states it:
// by the absence of a second "pipeline started" log line for this pipeline
// between edits — pkg/lifecycle.Service.Start (the only thing that emits
// that line) is called once at startup and, again, only by
// ApplyPlanLive's restart path. This is the same signal an operator would
// grep for in production, not a synthetic testing hook.
func TestRunDev_HotReload_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine dev hot-reload integration test in -short mode")
	}
	is := is.New(t)

	tmp := t.TempDir()
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
	pipelinePath := filepath.Join(pipelinesDir, "orders.yaml")
	outputPath := filepath.Join(tmp, "output.ndjson")

	is.NoErr(os.WriteFile(pipelinePath, []byte(pipelineYAML(outputPath, "50", "before")), 0o600))

	logs := &safeBuffer{}
	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false // not needed for this test; keeps startup fast and simple
	cfg.Pipelines.Path = pipelinesDir
	cfg.Dev.Enabled = true
	cfg.Log.Level = "debug"
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		zl := zerolog.New(logs).With().Timestamp().Logger().Level(l)
		return log.New(zl)
	}

	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(ctx) }()

	select {
	case <-r.Ready:
	case err := <-runDone:
		t.Fatalf("runtime exited before becoming ready: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not become ready in time")
	}

	// The pipeline was provisioned+started by normal startup provisioning,
	// not by the dev watcher (design doc: "startup provisioning runs
	// normally; the watcher handles subsequent edits").
	is.NoErr(waitForLines(outputPath, 3, 10*time.Second))
	is.Equal(startedCount(logs), 1)

	// --- 1. processor-only edit: applies in place ---
	linesBefore := countLines(t, outputPath)
	is.NoErr(os.WriteFile(pipelinePath, []byte(pipelineYAML(outputPath, "50", "after")), 0o600))

	// The AC: a processor-only edit applies IN PLACE — the new tag shows up
	// in the output with no pipeline restart. waitForTag only succeeds once
	// records carrying the new transform reach the destination, which can
	// only happen if the live swap actually took (ReconfigureProcessor ran
	// against the real processor.Service, not a mock).
	if err := waitForTag(outputPath, "after", 15*time.Second); err != nil {
		t.Fatalf("in-place apply did not land the new tag: %v\n\n=== LOGS ===\n%s", err, logs.String())
	}
	is.Equal(startedCount(logs), 1)                                   // no restart: still exactly the one startup Start
	is.True(strings.Contains(logs.String(), `"mode":"in_place"`))     // engine reported an in-place apply
	is.NoErr(waitForLines(outputPath, linesBefore+1, 10*time.Second)) // kept producing output

	// --- 2. source-setting edit: applies via a labeled restart ---
	// Changing the generator's rate is a connector setting, not a processor
	// setting, so it is restart-class: the engine tears the pipeline down and
	// rebuilds it rather than swapping a node in place.
	is.NoErr(os.WriteFile(pipelinePath, []byte(pipelineYAML(outputPath, "20", "after")), 0o600))

	deadline := time.Now().Add(15 * time.Second)
	for startedCount(logs) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	is.Equal(startedCount(logs), 2) // restarted: a second "pipeline started"
	is.True(strings.Contains(logs.String(), `"mode":"restart"`))

	linesAfterRestart := countLines(t, outputPath)
	is.NoErr(waitForLines(outputPath, linesAfterRestart+1, 10*time.Second)) // resumed producing output
	is.NoErr(waitForTag(outputPath, "after", 10*time.Second))               // the tag change landed one way or another

	// --- 3. syntax error: pipeline keeps running, untouched ---
	is.NoErr(os.WriteFile(pipelinePath, []byte("not: [valid: yaml:\n  -"), 0o600))

	// Give dev's debounce + a bad-apply attempt time to (not) happen.
	time.Sleep(500 * time.Millisecond)
	is.Equal(startedCount(logs), 2) // no further restart attempt
	is.True(strings.Contains(logs.String(), "dev: "))

	linesBeforeBadEdit := countLines(t, outputPath)
	is.NoErr(waitForLines(outputPath, linesBeforeBadEdit+1, 10*time.Second)) // still running, still producing

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}

// pipelineYAML renders a generator -> field.set -> file pipeline writing to
// outputPath. rate controls the generator's records/second (a source
// connector setting — changing it is restart-class); tagValue controls the
// field.set processor's static value (a processor setting — changing it is
// live-swappable).
func pipelineYAML(outputPath, rate, tagValue string) string {
	return fmt.Sprintf(`version: 2.2
pipelines:
  - id: dev-e2e
    status: running
    name: dev-e2e
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          rate: %q
          format.type: structured
          format.options.id: int
          operations: create
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %s
    processors:
      - id: tag
        plugin: builtin:field.set
        settings:
          field: .Payload.After.tag
          value: %q
`, rate, outputPath, tagValue)
}

func startedCount(logs *safeBuffer) int {
	return strings.Count(logs.String(), `"message":"pipeline started"`)
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("could not open %q: %v", path, err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

func waitForLines(path string, minLines int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.Open(path)
		if err == nil {
			n := 0
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) != "" {
					n++
				}
			}
			f.Close()
			if n >= minLines {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q to have at least %d line(s)", path, minLines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForTag(path, tag string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	needle := `"tag":"` + tag + `"`
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), needle) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q to contain %q", path, needle)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
