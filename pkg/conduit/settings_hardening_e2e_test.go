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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestSettingsHardening_EndToEnd runs real pipelines through a real runtime
// with the default (hardened) configuration and shows that attacker-style
// settings do nothing: the file connector cannot leave its directory, a
// ${VAR} in a setting does not expand the process environment, and the webhook
// processor cannot reach a loopback service. GHSA-m3mw-fpw8-w765.
//
// Each leak would show up within a second on an unhardened engine, so the
// absence checks below wait out a window long enough for it to have happened.
func TestSettingsHardening_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine hardening test in -short mode")
	}
	is := is.New(t)

	tmp := t.TempDir()
	filesDir := filepath.Join(tmp, "files")
	outsideDir := filepath.Join(tmp, "outside")
	is.NoErr(os.MkdirAll(filesDir, 0o755))
	is.NoErr(os.MkdirAll(outsideDir, 0o755))
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))

	const secret = "TOPSECRET-e2e-0xC0FFEE"
	t.Setenv("E2E_ENGINE_SECRET", secret)

	// A file outside the file connector's directory.
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	is.NoErr(os.WriteFile(outsideSecret, []byte("outside-file-"+secret+"\n"), 0o600))

	// A loopback service the webhook must not reach.
	var internalHits atomic.Int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		_, _ = w.Write([]byte("internal-" + secret))
	}))
	defer internal.Close()

	readOut := filepath.Join(filesDir, "read.out")
	envOut := filepath.Join(filesDir, "env-${E2E_ENGINE_SECRET}.out")
	hookOut := filepath.Join(filesDir, "hook.out")
	yaml := fmt.Sprintf(`version: 2.2
pipelines:
  - id: read-outside
    status: running
    name: read-outside
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %[1]s
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %[2]s
  - id: env-exfil
    status: running
    name: env-exfil
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          rate: "20"
          format.type: structured
          format.options.id: int
          operations: create
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %[3]s
  - id: ssrf
    status: running
    name: ssrf
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          rate: "20"
          format.type: structured
          format.options.id: int
          operations: create
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %[4]s
    processors:
      - id: hook
        plugin: builtin:webhook.http
        settings:
          request.url: %[5]q
          request.method: GET
          response.body: .Payload.After.leak
`, outsideSecret, readOut, envOut, hookOut, internal.URL)
	is.NoErr(os.WriteFile(filepath.Join(pipelinesDir, "p.yaml"), []byte(yaml), 0o600))

	logs := &safeBuffer{}
	cfg := conduit.DefaultConfigWithBasePath(tmp)
	cfg.DB.Type = conduit.DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Log.Level = "debug"
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		return log.New(zerolog.New(logs).With().Timestamp().Logger().Level(l))
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

	time.Sleep(5 * time.Second)

	// 1. file connector: nothing from the outside file reached the pipeline.
	// (Raw data is base64 in the destination's JSON lines, so check that the
	// output is empty rather than searching for the plaintext.)
	if b, err := os.ReadFile(readOut); err == nil {
		is.Equal(strings.TrimSpace(string(b)), "") // outside file content must not be read into the pipeline
	}

	// 2. env: no file was created under a name made of the secret.
	entries, err := os.ReadDir(filesDir)
	is.NoErr(err)
	for _, e := range entries {
		is.True(!strings.Contains(e.Name(), secret)) // ${VAR} must not expand the engine environment
	}

	// 3. SSRF: the loopback service saw nothing and nothing was written from it.
	is.Equal(internalHits.Load(), int64(0))
	if b, err := os.ReadFile(hookOut); err == nil {
		is.Equal(strings.TrimSpace(string(b)), "")
	}

	// The refusals surface in the engine log (the stable codes are asserted at
	// the unit level, where the structured error is available).
	l := logs.String()
	is.True(strings.Contains(l, "is not allowed: the path is outside every allowed directory")) // file refusal
	is.True(strings.Contains(l, "is not in the operator allowlist (env.allow)"))                // env refusal
	is.True(strings.Contains(l, "refused by the egress guard"))                                 // ssrf refusal

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}
