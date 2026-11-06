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

package conduit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
)

// TestRuntime_ArchV2_TrailingPartialBatch is the end-to-end regression test for
// #3231 (repro A of docs/design-documents/20261103-archv2-destination-batch-timing.md,
// scaled down): a generator emits a fixed number of records in source batches
// of 8192, the file destination batches 10000 with sdk.batch.delay 1s, and the
// source then goes quiet. The total is not a multiple of the destination batch,
// so the last records form a partial batch that no arrival will fill. On main
// they stayed unwritten and unacked until the pipeline was stopped. With the
// flush timer every record is written, and the source is acked through the
// last one, while the pipeline is still running. The test does not assert how
// soon (the funnel tests pin the deadline in fake time); its bound only stops
// a regression from hanging.
func TestRuntime_ArchV2_TrailingPartialBatch(t *testing.T) {
	// Two full source batches (16384 records, one size flush at the destination)
	// and a partial one of 5088, the tail of the smoke run, that stays below the
	// destination's 10000.
	const total = 2*8192 + 5088
	dir := t.TempDir()
	pipelinesDir := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.jsonl")
	y := fmt.Sprintf(`version: "2.2"
pipelines:
  - id: tail
    status: stopped
    connectors:
      - id: gen
        type: source
        plugin: builtin:generator
        settings:
          recordCount: "%d"
          format.type: structured
          format.options.id: int
          operations: create
          sdk.batch.size: "8192"
          sdk.batch.delay: "100ms"
          sdk.schema.extract.key.enabled: "false"
          sdk.schema.extract.payload.enabled: "false"
      - id: out
        type: destination
        plugin: builtin:file
        settings:
          path: %s
          sdk.batch.size: "10000"
          sdk.batch.delay: "1s"
`, total, out)
	if err := os.WriteFile(filepath.Join(pipelinesDir, "tail.yaml"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}

	logs := &trailingLogs{}
	cfg := DefaultConfigWithBasePath(dir)
	cfg.Connectors.File.AllowedDirs = []string{dir}
	cfg.DB.Type = DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Preview.PipelineArchV2 = true
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		return log.New(zerolog.New(logs).With().Timestamp().Logger().Level(l))
	}
	r, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(30 * time.Second):
			t.Error("runtime did not stop within 30s")
		}
	})
	select {
	case <-r.Ready:
	case err := <-runDone:
		t.Fatalf("runtime exited before ready: %v\nlogs:\n%s", err, logs.String())
	case <-ctx.Done():
		t.Fatalf("runtime not ready: %v", ctx.Err())
	}
	if r.ProvisioningErr != nil {
		t.Fatalf("provisioning: %v\nlogs:\n%s", r.ProvisioningErr, logs.String())
	}
	if err := r.Orchestrator.Pipelines.Start(ctx, "tail"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Every record written, and the source acked through the last one, with the
	// pipeline still running: nothing has asked it to stop yet.
	deadline := time.Now().Add(15 * time.Second)
	var lines int
	var last, acked opencdc.Position
	for time.Now().Before(deadline) {
		lines, last = fileTail(t, out)
		acked = sourcePosition(ctx, t, r, "tail:gen")
		if lines >= total && last != nil && bytes.Equal(acked, last) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lines != total {
		t.Fatalf("destination has %d of %d records before any stop (the trailing partial batch was never flushed)\nlogs:\n%s",
			lines, total, logs.String())
	}
	if !bytes.Equal(acked, last) {
		t.Fatalf("source position is %q, want the last written record's %q: the tail was written but not acked", acked, last)
	}
}

// fileTail returns the number of complete lines in path and the position of the
// record on the last one.
func fileTail(t *testing.T, path string) (int, opencdc.Position) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, nil
	}
	n := bytes.Count(b, []byte{'\n'})
	if n == 0 {
		return 0, nil
	}
	b = b[:bytes.LastIndexByte(b, '\n')] // complete lines only: a write may be in progress
	lastLine := b[bytes.LastIndexByte(b, '\n')+1:]
	var rec opencdc.Record
	if err := json.Unmarshal(lastLine, &rec); err != nil {
		t.Fatalf("decode last line %q: %v", lastLine, err)
	}
	return n, rec.Position
}

// sourcePosition returns the position the engine last acked to source id.
func sourcePosition(ctx context.Context, t *testing.T, r *Runtime, id string) opencdc.Position {
	t.Helper()
	inst, err := r.connectorService.Get(ctx, id)
	if err != nil {
		t.Fatalf("get source %s: %v", id, err)
	}
	inst.RLock()
	defer inst.RUnlock()
	st, _ := inst.State.(connector.SourceState)
	return st.Position
}

type trailingLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *trailingLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *trailingLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
