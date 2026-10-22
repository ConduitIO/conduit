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
// absence checks wait out a window long enough for it to have happened.
//
// One pipeline per runtime on purpose: a pipeline that fails while startup
// provisioning is still creating the next one trips an existing, unrelated data
// race on pipeline.Service's instance map under -race.
func TestSettingsHardening_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine hardening test in -short mode")
	}

	const secret = "TOPSECRET-e2e-0xC0FFEE"
	t.Setenv("E2E_ENGINE_SECRET", secret)

	// startRuntime provisions one pipeline file under a fresh data dir and
	// returns the data dir, the files dir and the engine log.
	startRuntime := func(t *testing.T, expect string, pipelineYAML func(dataDir, filesDir string) string) (string, string, *safeBuffer) {
		t.Helper()
		is := is.New(t)
		tmp := t.TempDir()
		filesDir := filepath.Join(tmp, "files")
		pipelinesDir := filepath.Join(tmp, "pipelines")
		is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
		is.NoErr(os.WriteFile(filepath.Join(pipelinesDir, "p.yaml"), []byte(pipelineYAML(tmp, filesDir)), 0o600))

		logs := &safeBuffer{}
		cfg := conduit.DefaultConfigWithBasePath(tmp) // the hardened defaults under test
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
		runDone := make(chan error, 1)
		go func() { runDone <- r.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-runDone:
			case <-time.After(20 * time.Second):
				t.Error("runtime did not shut down after context cancellation")
			}
		})
		select {
		case <-r.Ready:
		case err := <-runDone:
			t.Fatalf("runtime exited before becoming ready: %v", err)
		case <-time.After(20 * time.Second):
			t.Fatal("runtime did not become ready in time")
		}
		// Wait for the refusal to be logged (up to 30 s on a loaded machine), then
		// give a leak that would have started alongside it time to show up.
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(logs.String(), expect) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
		return tmp, filesDir, logs
	}

	t.Run("file connector cannot read outside its directory", func(t *testing.T) {
		is := is.New(t)
		var outsideSecret, readOut string
		_, _, logs := startRuntime(t, "is not allowed: the path is outside every allowed directory", func(dataDir, filesDir string) string {
			outsideDir := filepath.Join(dataDir, "outside")
			is.NoErr(os.MkdirAll(outsideDir, 0o755))
			is.NoErr(os.MkdirAll(filesDir, 0o755))
			outsideSecret = filepath.Join(outsideDir, "secret.txt")
			is.NoErr(os.WriteFile(outsideSecret, []byte("outside-file-"+secret+"\n"), 0o600))
			readOut = filepath.Join(filesDir, "read.out")
			return fmt.Sprintf(`version: 2.2
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
`, outsideSecret, readOut)
		})
		// Raw data is base64 in the destination's JSON lines, so check that the
		// output is empty rather than searching for the plaintext.
		if b, err := os.ReadFile(readOut); err == nil {
			is.Equal(strings.TrimSpace(string(b)), "") // outside file content must not be read into the pipeline
		}
		is.True(strings.Contains(logs.String(), "is not allowed: the path is outside every allowed directory"))
	})

	t.Run("generator file format cannot read outside the file directory", func(t *testing.T) {
		is := is.New(t)
		var genOut string
		_, _, logs := startRuntime(t, "is not allowed: the path is outside every allowed directory", func(dataDir, filesDir string) string {
			outsideDir := filepath.Join(dataDir, "outside")
			is.NoErr(os.MkdirAll(outsideDir, 0o755))
			is.NoErr(os.MkdirAll(filesDir, 0o755))
			outsideSecret := filepath.Join(outsideDir, "secret.txt")
			is.NoErr(os.WriteFile(outsideSecret, []byte("outside-file-"+secret+"\n"), 0o600))
			genOut = filepath.Join(filesDir, "gen.out")
			return fmt.Sprintf(`version: 2.2
pipelines:
  - id: gen-file
    status: running
    name: gen-file
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          rate: "20"
          operations: create
          format.type: file
          format.options.path: %[1]s
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %[2]s
`, outsideSecret, genOut)
		})
		if b, err := os.ReadFile(genOut); err == nil {
			is.Equal(strings.TrimSpace(string(b)), "") // the generator must not turn the outside file into records
		}
		is.True(strings.Contains(logs.String(), "is not allowed: the path is outside every allowed directory"))
	})

	t.Run("env reference does not expand the engine environment", func(t *testing.T) {
		is := is.New(t)
		_, filesDir, logs := startRuntime(t, "is not in the operator allowlist (env.allow)", func(_, filesDir string) string {
			is.NoErr(os.MkdirAll(filesDir, 0o755))
			return fmt.Sprintf(`version: 2.2
pipelines:
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
          path: %s
`, filepath.Join(filesDir, "env-${E2E_ENGINE_SECRET}.out"))
		})
		entries, err := os.ReadDir(filesDir)
		is.NoErr(err)
		for _, e := range entries {
			is.True(!strings.Contains(e.Name(), secret)) // ${VAR} must not expand the engine environment
		}
		is.True(strings.Contains(logs.String(), "is not in the operator allowlist (env.allow)"))
	})

	t.Run("webhook cannot reach a loopback service", func(t *testing.T) {
		is := is.New(t)
		var internalHits atomic.Int64
		internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			internalHits.Add(1)
			_, _ = w.Write([]byte("internal-" + secret))
		}))
		defer internal.Close()

		var hookOut string
		_, _, logs := startRuntime(t, "refused by the egress guard", func(_, filesDir string) string {
			is.NoErr(os.MkdirAll(filesDir, 0o755))
			hookOut = filepath.Join(filesDir, "hook.out")
			return fmt.Sprintf(`version: 2.2
pipelines:
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
          path: %[1]s
    processors:
      - id: hook
        plugin: builtin:webhook.http
        settings:
          request.url: %[2]q
          request.method: GET
          response.body: .Payload.After.leak
`, hookOut, internal.URL)
		})
		is.Equal(internalHits.Load(), int64(0)) // the loopback service must not be reached
		if b, err := os.ReadFile(hookOut); err == nil {
			is.Equal(strings.TrimSpace(string(b)), "")
		}
		is.True(strings.Contains(logs.String(), "refused by the egress guard"))
	})
}
