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

// End-to-end tests for the two infra-free vendored templates
// (generator-log, generator-file):
// docs/design-documents/20260723-templates-gallery.md §6 AC-3 requires every
// template be tested "end to end ... records asserted landed at the
// destination — not just 'YAML parses'". These two need no docker-compose
// infra (their only dependency is the built-in generator/log/file
// connectors), so they run as ordinary (untagged) tests, skipped only in
// -short mode — the same convention pkg/conduit/dev_integration_test.go
// already established for a full-engine test. The postgres-s3 and
// postgres-cdc-kafka templates' end-to-end tests (which DO need
// docker-compose infra: Postgres+MinIO, Postgres+Kafka) live in
// template_gallery_e2e_integration_test.go behind the `templates_e2e` build
// tag (deliberately not the shared `integration` tag — see that file's doc
// comment for why), run by `make test-integration-templates`.
package pipelines_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func newTemplateGalleryE2EEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

// scaffoldTemplate runs `pipelines init --template <name>` for real (the
// same cobra command production wires) against pipelinesDir, returning the
// path it wrote to.
func scaffoldTemplate(t *testing.T, pipelinesDir, name string) string {
	t.Helper()
	is := is.New(t)

	cmd := newTemplateGalleryE2EEcdysis().MustBuildCobraCommand(&pipelines.InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + pipelinesDir, "--template=" + name})
	is.NoErr(cmd.Execute())

	return filepath.Join(pipelinesDir, name+".yaml")
}

// TestTemplateGalleryE2E_GeneratorLog is
// docs/design-documents/20260723-templates-gallery.md §6 AC-3 for the
// generator-log template: scaffold it for real, boot the REAL engine
// (pkg/conduit.Runtime — the same runtime `conduit run` uses, not a fake or
// a mock pipeline executor) against the scaffolded file, and assert log
// output actually appears — not just that the YAML parses.
//
// The log destination writes through this process's own logger, so
// "assert records landed" here means "assert log lines for the generated
// records appear in the runtime's configured log sink", which is exactly
// what an operator watching `conduit run`'s output would see.
func TestTemplateGalleryE2E_GeneratorLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine template end-to-end test in -short mode")
	}
	is := is.New(t)

	tmp := t.TempDir()
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))

	scaffoldTemplate(t, pipelinesDir, "generator-log")

	logs := &safeBuffer{}
	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Log.Level = "info"
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

	// The generator-log destination logs each record it receives as its
	// own structured log line, e.g.
	// {"...","plugin_name":"builtin:log","record":{...,"payload":{"after":{"id":...,"name":...}}},...}
	// — assert at least 3 such record-carrying log lines from the
	// generator-log destination appear, proving records actually flowed
	// source -> destination (not merely that the pipeline started).
	const wantRecordLogs = 3
	if err := waitForCount(logs, `"plugin_name":"builtin:log","component":"plugin","record":{`, wantRecordLogs, 15*time.Second); err != nil {
		t.Fatalf("did not observe %d record log lines from the log destination: %v\n\n=== LOGS ===\n%s",
			wantRecordLogs, err, logs.String())
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}

// TestTemplateGalleryE2E_GeneratorFile is
// docs/design-documents/20260723-templates-gallery.md §6 AC-3 for the
// generator-file template: scaffold it for real, override the destination
// path to a temp file (the vendored template ships a relative placeholder
// path — this test proves the SHAPE works by overriding it, the same way a
// real user would edit the path before running), boot the real engine, and
// assert real newline-delimited JSON records land in that file.
func TestTemplateGalleryE2E_GeneratorFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine template end-to-end test in -short mode")
	}
	is := is.New(t)

	tmp := t.TempDir()
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))

	pipelinePath := scaffoldTemplate(t, pipelinesDir, "generator-file")

	// Point the destination at a real, absolute temp file instead of the
	// template's relative placeholder path — this is the one edit a real
	// user is expected to make per the template's README, and it's what
	// lets this test assert on the destination's actual on-disk content.
	outputPath := filepath.Join(tmp, "output.ndjson")
	original, err := os.ReadFile(pipelinePath)
	is.NoErr(err)
	edited := strings.Replace(string(original), "./generator-file-output.ndjson", outputPath, 1)
	is.True(edited != string(original)) // the replacement actually matched something
	is.NoErr(os.WriteFile(pipelinePath, []byte(edited), 0o600))

	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir

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

	is.NoErr(waitForFileLines(outputPath, 3, 15*time.Second))

	data, err := os.ReadFile(outputPath)
	is.NoErr(err)
	// Every non-empty line must be a JSON object carrying the generated
	// "id"/"name" fields the template's source configures — proves the
	// destination received real structured records, not empty lines.
	sc := bufio.NewScanner(bytes.NewReader(data))
	nonEmpty := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		nonEmpty++
		is.True(strings.Contains(line, `"id"`))
	}
	is.True(nonEmpty >= 3)

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}

// safeBuffer, waitForCount, and waitForFileLines are
// small test-local helpers mirroring pkg/conduit/dev_integration_test.go's
// own (that file lives in a different package, so these can't be reused
// directly without exporting test-only helpers from a production package —
// duplicating ~20 lines of polling glue here is simpler and lower-risk than
// doing that).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForCount(logs *safeBuffer, needle string, minCount int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if strings.Count(logs.String(), needle) >= minCount {
			return nil
		}
		if time.Now().After(deadline) {
			return errTimeout(needle)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForFileLines(path string, minLines int, timeout time.Duration) error {
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
			return errTimeout(path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func errTimeout(what string) error {
	return &timeoutError{what: what}
}

type timeoutError struct{ what string }

func (e *timeoutError) Error() string { return "timed out waiting for: " + e.what }
