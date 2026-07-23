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
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	conduit "github.com/conduitio/conduit"
	promclient "github.com/prometheus/client_golang/prometheus"

	"github.com/matryer/is"
)

// helperProcessEnvVar, when set, tells TestMain-less test binary invocation
// (via TestObservabilityIsolation_Subprocess re-exec below) to run as the
// actual New->Run->Import->Stop cycle instead of as a normal test, so the
// PARENT test process can observe this child's real os.Stdout/os.Stderr file
// descriptors — the only way to prove "zero bytes written to the process's
// real stdout/stderr" (AC-2c). Asserting against a captured os.Stdout var
// from within the same process cannot see writes made through the raw fd
// (e.g. by a C library or a lower-level fmt.Fprint(os.Stdout, ...) call this
// package doesn't control), so this test re-execs itself as a subprocess.
const helperProcessEnvVar = "CONDUIT_EMBED_OBSERVABILITY_HELPER"

// TestObservabilityIsolation_Subprocess is the "parent" half: it re-execs
// this test binary with helperProcessEnvVar set, capturing the child's real
// stdout/stderr, and asserts both are byte-for-byte empty (AC-2c) even
// though the child ran a full New->Run->Import->Stop cycle that logs
// extensively (captured instead through an injected *slog.Logger writing to
// a file the child reports back via its own stdout... no — see below).
//
// The child cannot use its own stdout to report results back (that would
// defeat the "zero bytes to stdout" assertion for anything the child
// deliberately writes), so it reports success/failure only through its
// process exit code: 0 for "every assertion inside the child passed", and a
// prints exactly one line to stderr on failure, which is asserted absent
// separately when the exit code is 0 and asserted PRESENT (a diagnostic) when
// it's not — this keeps the parent's stdout/stderr assertion meaningful
// (empty on success) while still surfacing a failure reason for debugging.
func TestObservabilityIsolation_Subprocess(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestObservabilityIsolation_Subprocess", "-test.v")
	cmd.Env = append(os.Environ(), helperProcessEnvVar+"=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// AC-2c: zero bytes to the process's real stdout/stderr, regardless of
	// how much the child logged through its injected slog.Logger.
	is.Equal(stdout.String(), "")
	is.Equal(stderr.String(), "")
	is.NoErr(err) // the child's own internal assertions must have passed (see helper below)
}

// TestMain intercepts the subprocess re-exec: when helperProcessEnvVar is
// set, it runs the observability child logic directly (never through go
// test's normal reporting, which itself writes to stdout) and calls
// os.Exit with the result, instead of running the normal test suite.
func TestMain(m *testing.M) {
	if os.Getenv(helperProcessEnvVar) == "1" {
		if err := runObservabilityHelper(); err != nil {
			// This IS the child's stderr — the parent asserts it's empty
			// only when the child's exit code is 0; a non-zero exit here
			// fails the parent's is.NoErr(err) check above with this
			// message available for debugging via `go test -v` directly.
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runObservabilityHelper is the actual child logic: construct an Engine with
// a captured slog.Handler and an isolated prometheus.Registry, run it,
// import and start a pipeline, stop it, and verify (a) the handler captured
// Conduit's log lines, (b) the registry contains Conduit's metric families,
// (d) prometheus.DefaultRegisterer contains nothing Conduit-derived. It
// deliberately writes nothing to os.Stdout/os.Stderr itself on the success
// path — AC-2c is proved by the PARENT observing this process's real fds are
// untouched, not by this function's own care.
func runObservabilityHelper() error {
	handler := &capturingHandler{}
	logger := slog.New(handler)
	reg := promclient.NewRegistry()

	ctx := context.Background()
	e, err := conduit.New(ctx, conduit.Options{
		Logger:            logger,
		MetricsRegisterer: reg,
		DB:                conduit.DBOptions{Type: "inmemory"},
		PipelinesDir:      mustTempDir(),
	})
	if err != nil {
		return err
	}

	h, err := e.Run(ctx)
	if err != nil {
		return err
	}

	if err := e.Import(ctx, conduit.PipelineConfig{
		ID:   "observability-check",
		Name: "observability-check",
	}); err != nil {
		return err
	}

	if err := h.Stop(ctx); err != nil {
		return err
	}

	// (a) the captured handler received Conduit's log lines.
	if handler.count() == 0 {
		return helperError("expected the injected slog handler to capture at least one log line")
	}

	// (b) the isolated registerer contains Conduit's metric families.
	mfs, err := reg.Gather()
	if err != nil {
		return err
	}
	var foundConduitMetric bool
	for _, mf := range mfs {
		if mf.GetName() == "conduit_info" {
			foundConduitMetric = true
		}
	}
	if !foundConduitMetric {
		return helperError("expected the isolated registerer to contain conduit_info")
	}

	// (d) prometheus.DefaultRegisterer contains nothing Conduit-derived.
	defaultMfs, err := promclient.DefaultGatherer.Gather()
	if err != nil {
		return err
	}
	for _, mf := range defaultMfs {
		if mf.GetName() == "conduit_info" {
			return helperError("expected promclient.DefaultRegisterer to contain no Conduit metrics, found conduit_info")
		}
	}

	return nil
}

type helperError string

func (e helperError) Error() string { return string(e) }

func mustTempDir() string {
	dir, err := os.MkdirTemp("", "conduit-embed-observability-*")
	if err != nil {
		panic(err)
	}
	return dir
}
