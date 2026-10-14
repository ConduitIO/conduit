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

package provisioning

// Downgrade guard (decision D9 of design 20261014-secrets-at-rest.md): a
// database written by Conduit v0.21 or later stores ${VAR} templates under a
// SettingRefs key and removes the matching keys from Settings. This release
// must never start such a pipeline. These tests seed that shape into real
// stores (inmemory, badger, SQLite), then boot the real provisioning and
// lifecycle services over it, the way a downgraded binary would.
//
// Postgres is not covered: no store test in this repository runs against it,
// and the guard sits above the database.DB seam, so the encoded bytes are the
// only thing a backend contributes.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

const dgPipelineFile = `version: 2.0
pipelines:
  - id: dg1
    status: running
    name: dg-name
    description: downgrade guard
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        name: file-src
        settings:
          path: %[1]s
      - id: dst
        type: destination
        plugin: builtin:file
        name: file-dst
        settings:
          path: %[2]s
        processors:
          - id: cproc
            plugin: builtin:field.exclude
            settings:
              fields: '.Metadata["opencdc.readAt"]'
    processors:
      - id: pproc
        plugin: builtin:field.exclude
        settings:
          fields: '.Metadata["opencdc.createdAt"]'
    dead-letter-queue:
      plugin: builtin:log
      settings:
        message: failed
`

// dgTemplate is the v0.21-shaped value: the original template text.
const dgTemplate = "${DG_VALUE}"

type dgBackend struct {
	name string
	open func(t *testing.T) database.DB
}

var dgBackends = []dgBackend{
	{"inmemory", func(*testing.T) database.DB { return &inmemory.DB{} }},
	{"badger", func(t *testing.T) database.DB {
		db, err := badger.New(zerolog.Nop(), filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}},
	{"sqlite", func(t *testing.T) database.DB {
		db, err := sqlite.New(context.Background(), zerolog.Nop(), filepath.Join(t.TempDir(), "test.db"), "conduit_kv_store")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}},
}

// dgCountingPlugins counts connector plugin dispensers requested. A pipeline
// that is refused must request none: no plugin is started.
type dgCountingPlugins struct {
	*conn_plugin.PluginService
	dispensers atomic.Int32
}

func (c *dgCountingPlugins) NewDispenser(l log.CtxLogger, name, connID string) (conn_plugin.Dispenser, error) {
	c.dispensers.Add(1)
	return c.PluginService.NewDispenser(l, name, connID)
}

type dgBoot struct {
	db        database.DB
	plugins   *dgCountingPlugins
	pipelines *pipeline.Service
	conns     *connector.Service
	procs     *processor.Service
	lc        *lifecycle.Service
	prov      *Service
}

// dgStart boots every service over db, as a fresh process would.
func dgStart(t *testing.T, db database.DB, pipelinesPath string) *dgBoot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := log.Nop()

	reg, err := schemaregistry.NewSchemaRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	tokens := connutils.NewAuthManager()
	plugins := &dgCountingPlugins{PluginService: conn_plugin.NewPluginService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultBuiltinConnectors, connutils.NewSchemaService(logger, reg, tokens)),
		standalone.NewRegistry(logger, ""),
		tokens,
	)}
	plugins.Init(ctx, "conn-utils-token:12345", server.DefaultMaxReceiveRecordSize)

	b := &dgBoot{db: db, plugins: plugins}
	b.pipelines = pipeline.NewService(logger, db)
	persister := connector.NewPersister(logger, db, time.Second, 3)
	b.conns = connector.NewService(logger, db, persister)
	b.procs = processor.NewService(logger, db, proc_plugin.NewPluginService(
		logger, proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, reg), nil))
	b.lc = lifecycle.NewService(logger, &lifecycle.ErrRecoveryCfg{
		MinDelay: time.Second, MaxDelay: time.Minute, BackoffFactor: 2, MaxRetriesWindow: time.Minute,
	}, b.conns, b.procs, plugins, b.pipelines)
	b.prov = NewService(db, logger, b.pipelines, b.conns, b.procs, plugins, b.lc, pipelinesPath)

	for _, init := range []func(context.Context) error{b.procs.Init, b.conns.Init, b.pipelines.Init} {
		if err := init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		b.lc.StopAll(context.Background(), nil)
		_ = b.lc.Wait(10 * time.Second)
		persister.Wait()
	})
	return b
}

// dgWrite writes the pipelines dir and returns it plus the source file path.
func dgWrite(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := dgPipelineFileFor(src, filepath.Join(dir, "dst.txt"))
	if err := os.WriteFile(filepath.Join(dir, "p.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dgPipelineFileFor(src, dst string) string {
	r := strings.NewReplacer("%[1]s", src, "%[2]s", dst)
	return r.Replace(dgPipelineFile)
}

// dgSeed provisions the pipeline on db, then marks it SystemStopped (it was
// running when the v0.21 binary stopped) and rewrites the entity's record the
// way v0.21 does: key moved from Settings to SettingRefs, under refsKey.
func dgSeed(t *testing.T, db database.DB, dir, entity, refsKey string) {
	t.Helper()
	ctx := context.Background()
	// status: stopped, so seeding starts nothing.
	seedDir := t.TempDir()
	body, err := os.ReadFile(filepath.Join(dir, "p.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "p.yml"), []byte(strings.Replace(string(body), "status: running", "status: stopped", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	b := dgStart(t, db, seedDir)
	if err := b.prov.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.pipelines.UpdateStatus(ctx, "dg1", pipeline.StatusSystemStopped, ""); err != nil {
		t.Fatal(err)
	}

	var key, settingsPath, moved string
	switch entity {
	case "connector":
		key, settingsPath, moved = "connector:instance:dg1:dst", "Config", "path"
	case "connector-processor":
		key, settingsPath, moved = "processor:instance:dg1:dst:cproc", "Config", "fields"
	case "pipeline-processor":
		key, settingsPath, moved = "processor:instance:dg1:pproc", "Config", "fields"
	case "dlq":
		key, settingsPath, moved = "pipeline:instance:dg1", "DLQ", "message"
	case "last-active-config":
		key, settingsPath, moved = "connector:instance:dg1:dst", "LastActiveConfig", ""
	default:
		t.Fatalf("unknown entity %q", entity)
	}
	raw, err := db.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	cfg, _ := rec[settingsPath].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
		rec[settingsPath] = cfg
	}
	if moved != "" {
		settings, _ := cfg["Settings"].(map[string]any)
		if _, ok := settings[moved]; !ok {
			t.Fatalf("%s: setting %q not found in %v", key, moved, settings)
		}
		delete(settings, moved)
	} else {
		moved = "path"
	}
	cfg[refsKey] = map[string]any{moved: dgTemplate}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set(ctx, key, out); err != nil {
		t.Fatal(err)
	}
}

func dgSnapshot(t *testing.T, db database.DB) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, prefix := range []string{"connector:instance:", "processor:instance:"} {
		keys, err := db.GetKeys(context.Background(), prefix)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			v, err := db.Get(context.Background(), k)
			if err != nil {
				t.Fatal(err)
			}
			snap[k] = string(v)
		}
	}
	return snap
}

// dgHasRefs reports whether the stored record under key still carries a
// non-empty SettingRefs-like key (any case) anywhere in its JSON.
func dgHasRefs(t *testing.T, db database.DB, key string) bool {
	t.Helper()
	raw, err := db.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(strings.ToLower(string(raw)), `"settingrefs":{"`)
}

func TestDowngradeGuard_SettingRefsRefusesPipeline(t *testing.T) {
	entities := []struct{ name, recordKey string }{
		{"connector", "connector:instance:dg1:dst"},
		{"connector-processor", "processor:instance:dg1:dst:cproc"},
		{"pipeline-processor", "processor:instance:dg1:pproc"},
		{"dlq", "pipeline:instance:dg1"},
	}
	// How the v0.20.x boot reaches the lifecycle: the reconcile fails for the
	// pipeline, or the pipelines path is not there at all.
	scenarios := []struct {
		name string
		dir  func(t *testing.T, good string) string
	}{
		{"failed reconcile", func(t *testing.T, good string) string {
			bad := t.TempDir()
			body, err := os.ReadFile(filepath.Join(good, "p.yml"))
			if err != nil {
				t.Fatal(err)
			}
			// An invalid connector type fails validation for this pipeline.
			broken := strings.Replace(string(body), "type: destination", "type: nonsense", 1)
			if err := os.WriteFile(filepath.Join(bad, "p.yml"), []byte(broken), 0o600); err != nil {
				t.Fatal(err)
			}
			return bad
		}},
		{"missing pipelines path", func(t *testing.T, _ string) string { return filepath.Join(t.TempDir(), "nope") }},
	}

	for _, be := range dgBackends {
		for _, ent := range entities {
			for _, sc := range scenarios {
				t.Run(be.name+"/"+ent.name+"/"+sc.name, func(t *testing.T) {
					is := is.New(t)
					ctx := context.Background()
					db := be.open(t)
					good := dgWrite(t)
					dgSeed(t, db, good, ent.name, "SettingRefs")
					before := dgSnapshot(t, db)

					b := dgStart(t, db, sc.dir(t, good))
					is.True(b.prov.Init(ctx) != nil) // the reconcile did not heal it

					err := b.lc.Init(ctx) // auto-resume of the SystemStopped pipeline
					is.True(err != nil)
					ce, ok := conduiterr.Get(err)
					if !ok {
						t.Fatalf("no coded error in %v", err)
					}
					is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
					is.True(ce.Suggestion != "")
					is.True(ce.ConfigPath != "")
					for _, want := range []string{"dg1", "dg-name", "v0.21 or later", "re-provision"} {
						is.True(strings.Contains(ce.Message, want)) // names the pipeline and the fix
					}
					is.True(!strings.Contains(ce.Message, dgTemplate)) // names the settings, never the template

					pl, err := b.pipelines.Get(ctx, "dg1")
					is.NoErr(err)
					is.Equal(pl.GetStatus(), pipeline.StatusDegraded)
					is.True(strings.HasPrefix(pl.Error, "pipeline.settings_references_unsupported: "))
					is.Equal(b.plugins.dispensers.Load(), int32(0)) // no plugin was started

					// Stored bytes are untouched, so upgrading again works.
					is.Equal(dgSnapshot(t, db), before)
					is.True(dgHasRefs(t, db, ent.recordKey))
					// A user Start is refused the same way.
					err = b.lc.Start(ctx, "dg1")
					ce, ok = conduiterr.Get(err)
					is.True(ok)
					is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
					is.Equal(b.plugins.dispensers.Load(), int32(0))
				})
			}
		}
	}
}

// The JSON key is decoded case-insensitively, so every casing is refused and
// survives a re-write of the record.
func TestDowngradeGuard_KeyCasing(t *testing.T) {
	for _, key := range []string{"SettingRefs", "settingrefs", "sEtTiNgReFs", "SETTINGREFS"} {
		t.Run(key, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			db := &inmemory.DB{}
			good := dgWrite(t)
			dgSeed(t, db, good, "connector", key)

			b := dgStart(t, db, filepath.Join(t.TempDir(), "nope"))
			err := b.lc.Start(ctx, "dg1")
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
			is.Equal(b.plugins.dispensers.Load(), int32(0))

			// Re-writing the connector (what a state or position flush does)
			// keeps the field, under the canonical name.
			c, err := b.conns.Get(ctx, "dg1:dst")
			is.NoErr(err)
			is.Equal(c.Config.SettingRefs, map[string]string{"path": dgTemplate})
			is.NoErr(connector.NewStore(db, log.Nop()).Set(ctx, c.ID, c))
			raw, err := db.Get(ctx, "connector:instance:dg1:dst")
			is.NoErr(err)
			var rec struct {
				Config struct{ SettingRefs map[string]string }
			}
			is.NoErr(json.Unmarshal(raw, &rec))
			is.Equal(rec.Config.SettingRefs, map[string]string{"path": dgTemplate})
		})
	}
}

// A LastActiveConfig that still carries SettingRefs is kept on re-write but
// does not refuse the pipeline: Config is what would run, and refusing on the
// stale copy would stop a successful reconcile from healing the pipeline.
func TestDowngradeGuard_LastActiveConfigOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	good := dgWrite(t)
	dgSeed(t, db, good, "last-active-config", "SettingRefs")

	b := dgStart(t, db, good)
	c, err := b.conns.Get(ctx, "dg1:dst")
	is.NoErr(err)
	is.Equal(c.LastActiveConfig.SettingRefs, map[string]string{"path": dgTemplate})
	is.NoErr(connector.NewStore(db, log.Nop()).Set(ctx, c.ID, c))
	is.True(dgHasRefs(t, db, "connector:instance:dg1:dst"))

	is.NoErr(b.lc.Start(ctx, "dg1")) // not refused
	is.True(b.plugins.dispensers.Load() > 0)
}

// A successful reconcile replaces the stored config with the file's, so the
// record no longer carries SettingRefs and the pipeline runs (the design's
// self-heal path).
func TestDowngradeGuard_SuccessfulReconcileHeals(t *testing.T) {
	for _, ent := range []string{"connector", "connector-processor", "pipeline-processor", "dlq"} {
		t.Run(ent, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			db := &inmemory.DB{}
			good := dgWrite(t)
			dgSeed(t, db, good, ent, "SettingRefs")

			b := dgStart(t, db, good)
			is.NoErr(b.prov.Init(ctx)) // reconcile, and start (status: running)
			is.True(b.plugins.dispensers.Load() > 0)
			for _, k := range []string{"connector:instance:dg1:dst", "processor:instance:dg1:dst:cproc", "processor:instance:dg1:pproc", "pipeline:instance:dg1"} {
				is.True(!dgHasRefs(t, db, k)) // healed: literals are back
			}
		})
	}
}

// A record without SettingRefs is unchanged: nothing is refused and the
// pipeline starts.
func TestDowngradeGuard_NoSettingRefsUnchanged(t *testing.T) {
	for _, be := range dgBackends {
		t.Run(be.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			db := be.open(t)
			good := dgWrite(t)
			b := dgStart(t, db, good)
			is.NoErr(b.prov.Init(ctx))
			is.True(b.plugins.dispensers.Load() > 0)
			pl, err := b.pipelines.Get(ctx, "dg1")
			is.NoErr(err)
			is.Equal(pl.Error, "")
			is.True(pl.GetStatus() != pipeline.StatusDegraded)
		})
	}
}
