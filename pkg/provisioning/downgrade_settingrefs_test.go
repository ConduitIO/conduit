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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
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
	"github.com/conduitio/conduit/pkg/provisioning/config"
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
// that is refused must request none: no plugin is started. It also records the
// configs a destination plugin receives in its OnUpdated and OnDeleted
// lifecycle events.
type dgCountingPlugins struct {
	*conn_plugin.PluginService
	dispensers atomic.Int32

	mu      sync.Mutex
	deleted []map[string]string
	updated [][2]map[string]string
}

func (c *dgCountingPlugins) NewDispenser(l log.CtxLogger, name, connID string) (conn_plugin.Dispenser, error) {
	c.dispensers.Add(1)
	d, err := c.PluginService.NewDispenser(l, name, connID)
	if err != nil {
		return nil, err
	}
	return dgDispenser{Dispenser: d, rec: c}, nil
}

func (c *dgCountingPlugins) lifecycleEvents() (deleted int, updated [][2]map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deleted), append([][2]map[string]string(nil), c.updated...)
}

type dgDispenser struct {
	conn_plugin.Dispenser
	rec *dgCountingPlugins
}

func (d dgDispenser) DispenseDestination() (conn_plugin.DestinationPlugin, error) {
	p, err := d.Dispenser.DispenseDestination()
	if err != nil {
		return nil, err
	}
	return dgDestination{DestinationPlugin: p, rec: d.rec}, nil
}

type dgDestination struct {
	conn_plugin.DestinationPlugin
	rec *dgCountingPlugins
}

func (p dgDestination) LifecycleOnDeleted(ctx context.Context, r pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	p.rec.mu.Lock()
	p.rec.deleted = append(p.rec.deleted, r.Config)
	p.rec.mu.Unlock()
	return p.DestinationPlugin.LifecycleOnDeleted(ctx, r)
}

func (p dgDestination) LifecycleOnUpdated(ctx context.Context, r pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	p.rec.mu.Lock()
	p.rec.updated = append(p.rec.updated, [2]map[string]string{r.ConfigBefore, r.ConfigAfter})
	p.rec.mu.Unlock()
	return p.DestinationPlugin.LifecycleOnUpdated(ctx, r)
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

// dgSeed builds the database a downgrade starts from. It runs the pipeline for
// real on db (a source and a destination copying a file, so both connectors
// have a position and a LastActiveConfig), stops it, marks it SystemStopped as
// a shutdown does, and then rewrites the records the way v0.21 stores them: the
// setting moved from Settings to SettingRefs, under refsKey, in Config and in
// LastActiveConfig.
//
// entity selects what v0.21 rewrote: "connector" (the source and the
// destination), "connector-processor", "pipeline-processor", "dlq", or
// "last-active-config" (only the destination's LastActiveConfig).
func dgSeed(t *testing.T, db database.DB, dir, entity, refsKey string) {
	t.Helper()
	ctx := context.Background()

	b := dgStart(t, db, dir)
	if err := b.prov.Init(ctx); err != nil {
		t.Fatal(err)
	}
	dgWaitDelivered(t, db, dir)
	if err := b.lc.StopAndWait(ctx, "dg1"); err != nil {
		t.Fatal(err)
	}
	if err := b.pipelines.UpdateStatus(ctx, "dg1", pipeline.StatusSystemStopped, ""); err != nil {
		t.Fatal(err)
	}

	rewrite := func(key string, fn func(rec map[string]any)) {
		raw, err := db.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		fn(rec)
		out, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Set(ctx, key, out); err != nil {
			t.Fatal(err)
		}
	}
	// move takes setting out of rec[field].Settings and stores its template in
	// rec[field][refsKey].
	move := func(rec map[string]any, field, setting string) {
		cfg, _ := rec[field].(map[string]any)
		settings, _ := cfg["Settings"].(map[string]any)
		if _, ok := settings[setting]; !ok {
			t.Fatalf("%s.Settings has no %q: %v", field, setting, settings)
		}
		delete(settings, setting)
		cfg[refsKey] = map[string]any{setting: dgTemplate}
	}

	switch entity {
	case "connector":
		for _, key := range []string{"connector:instance:dg1:src", "connector:instance:dg1:dst"} {
			rewrite(key, func(rec map[string]any) {
				move(rec, "Config", "path")
				move(rec, "LastActiveConfig", "path")
			})
		}
	case "last-active-config":
		rewrite("connector:instance:dg1:dst", func(rec map[string]any) { move(rec, "LastActiveConfig", "path") })
	case "connector-processor":
		rewrite("processor:instance:dg1:dst:cproc", func(rec map[string]any) { move(rec, "Config", "fields") })
	case "pipeline-processor":
		rewrite("processor:instance:dg1:pproc", func(rec map[string]any) { move(rec, "Config", "fields") })
	case "dlq":
		rewrite("pipeline:instance:dg1", func(rec map[string]any) { move(rec, "DLQ", "message") })
	default:
		t.Fatalf("unknown entity %q", entity)
	}
}

// dgWaitDelivered waits until the seed pipeline has delivered a record and
// the source's position is stored.
func dgWaitDelivered(t *testing.T, db database.DB, dir string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for {
		src, err := db.Get(ctx, "connector:instance:dg1:src")
		dst, derr := os.ReadFile(filepath.Join(dir, "dst.txt"))
		if err == nil && derr == nil && len(dst) > 0 && strings.Contains(string(src), `"Position":"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the seed pipeline to deliver a record and store a position")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dgRefCount counts the SettingRefs objects in the record stored under key,
// whatever the key's casing.
func dgRefCount(t *testing.T, db database.DB, key string) int {
	t.Helper()
	raw, err := db.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(strings.ToLower(string(raw)), `"settingrefs":{"`)
}

// dgRewriteAll re-saves every stored connector through connector.Store, as a
// position or last-active-config flush does (Invariant 6: it must keep
// SettingRefs).
func dgRewriteAll(t *testing.T, b *dgBoot) {
	t.Helper()
	ctx := context.Background()
	st := connector.NewStore(b.db, log.Nop())
	for _, id := range []string{"dg1:src", "dg1:dst"} {
		c, err := b.conns.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Set(ctx, id, c); err != nil {
			t.Fatal(err)
		}
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

					// A flush of the connectors (what the position persister does)
					// must not drop the references either. This fails if
					// connector.Store.PrepareSet forgets SettingRefs.
					dgRewriteAll(t, b)
					is.True(dgHasRefs(t, db, ent.recordKey))
					if ent.name == "connector" {
						for _, k := range []string{"connector:instance:dg1:src", "connector:instance:dg1:dst"} {
							is.Equal(dgRefCount(t, db, k), 2) // Config and LastActiveConfig
						}
					}
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
				is.True(!dgConfigRefs(t, db, k)) // healed: literals are back in Config
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

// writeBroken writes a pipelines dir whose file fails only at a late action:
// an extra pipeline processor with an unknown plugin. The update actions for
// the connectors, processors and DLQ run first and succeed, the create fails,
// and importPipeline rolls back (B1: the rollback replays config.*, which has
// no SettingRefs).
func dgWriteLateFailure(t *testing.T, good string) string {
	t.Helper()
	bad := t.TempDir()
	body, err := os.ReadFile(filepath.Join(good, "p.yml"))
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(body), "    processors:\n      - id: pproc\n",
		"    processors:\n      - id: zzbad\n        plugin: builtin:does.not.exist\n      - id: pproc\n", 1)
	if broken == string(body) {
		t.Fatal("could not inject the failing processor")
	}
	if err := os.WriteFile(filepath.Join(bad, "p.yml"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	return bad
}

// B1: a reconcile whose update actions succeed and whose later action fails is
// rolled back from config.*. The in-memory entities must keep their
// SettingRefs, so the guard still refuses and the pipeline never runs with the
// keys absent.
func TestDowngradeGuard_RollbackKeepsSettingRefs(t *testing.T) {
	for _, be := range dgBackends {
		for _, ent := range []string{"connector", "connector-processor", "pipeline-processor", "dlq"} {
			t.Run(be.name+"/"+ent, func(t *testing.T) {
				is := is.New(t)
				ctx := context.Background()
				db := be.open(t)
				good := dgWrite(t)
				dgSeed(t, db, good, ent, "SettingRefs")
				before := dgSnapshot(t, db)

				b := dgStart(t, db, dgWriteLateFailure(t, good))
				perr := b.prov.Init(ctx)
				is.True(perr != nil)
				is.True(strings.Contains(perr.Error(), "does.not.exist") || strings.Contains(perr.Error(), "zzbad")) // it failed late, not in validation

				// The entity in memory still carries its references.
				switch ent {
				case "connector":
					for _, id := range []string{"dg1:src", "dg1:dst"} {
						c, err := b.conns.Get(ctx, id)
						is.NoErr(err)
						is.Equal(c.Config.SettingRefs, map[string]string{"path": dgTemplate})
						is.Equal(c.LastActiveConfig.SettingRefs, map[string]string{"path": dgTemplate})
					}
				case "connector-processor":
					p, err := b.procs.Get(ctx, "dg1:dst:cproc")
					is.NoErr(err)
					is.Equal(p.Config.SettingRefs, map[string]string{"fields": dgTemplate})
				case "pipeline-processor":
					p, err := b.procs.Get(ctx, "dg1:pproc")
					is.NoErr(err)
					is.Equal(p.Config.SettingRefs, map[string]string{"fields": dgTemplate})
				case "dlq":
					pl, err := b.pipelines.Get(ctx, "dg1")
					is.NoErr(err)
					is.Equal(pl.DLQ.SettingRefs, map[string]string{"message": dgTemplate})
				}

				err := b.lc.Init(ctx) // auto-resume
				ce, ok := conduiterr.Get(err)
				is.True(ok)
				is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
				pl, err := b.pipelines.Get(ctx, "dg1")
				is.NoErr(err)
				is.Equal(pl.GetStatus(), pipeline.StatusDegraded)
				is.Equal(b.plugins.dispensers.Load(), int32(0))

				// Stored bytes are those the downgrade started from, and survive a flush.
				is.Equal(dgSnapshot(t, db), before)
				dgRewriteAll(t, b)
				is.True(dgRefCount(t, db, "connector:instance:dg1:dst")+dgRefCount(t, db, "processor:instance:dg1:pproc")+
					dgRefCount(t, db, "processor:instance:dg1:dst:cproc")+dgRefCount(t, db, "pipeline:instance:dg1") > 0)
			})
		}
	}
}

// B2: removing the connector from the pipelines (an empty or renamed dir, or a
// config without it) must not run the destination's delete hook with the
// reference-backed keys missing: the connector, its record and the plugin are
// left alone, and the error is coded.
func TestDowngradeGuard_DeleteRefusedWithSettingRefs(t *testing.T) {
	for _, be := range dgBackends {
		for _, entity := range []string{"connector", "last-active-config"} {
			t.Run(be.name+"/"+entity+"/empty pipelines dir", func(t *testing.T) {
				is := is.New(t)
				ctx := context.Background()
				db := be.open(t)
				dgSeed(t, db, dgWrite(t), entity, "SettingRefs")
				before := dgSnapshot(t, db)

				b := dgStart(t, db, t.TempDir()) // mounted, but the file is gone
				err := b.prov.Init(ctx)          // deleteOldPipelines
				is.NoErr(err)                    // it logs the refusal and retries next boot

				deleted, _ := b.plugins.lifecycleEvents()
				is.Equal(deleted, 0)                            // OnDelete never ran
				is.Equal(b.plugins.dispensers.Load(), int32(0)) // no plugin was even dispensed
				is.Equal(dgSnapshot(t, db), before)             // record kept
				_, err = b.conns.Get(ctx, "dg1:dst")
				is.NoErr(err)
				_, err = b.pipelines.Get(ctx, "dg1")
				is.NoErr(err)

				// The same refusal, directly and through provisioning's Delete.
				err = b.conns.Delete(ctx, "dg1:dst", b.plugins)
				ce, ok := conduiterr.Get(err)
				is.True(ok)
				is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
				err = b.prov.Delete(ctx, "dg1")
				ce, ok = conduiterr.Get(err)
				is.True(ok)
				is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
				is.Equal(dgSnapshot(t, db), before) // the processors were not deleted first
			})
		}
	}
}

// The reconcile delete action (what dev hot-reload and a config that drops the
// connector run) is refused the same way and leaves the connector in place.
func TestDowngradeGuard_ImportDroppingConnectorRefused(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	good := dgWrite(t)
	dgSeed(t, db, good, "connector", "SettingRefs")
	before := dgConnectorRecords(dgSnapshot(t, db))

	b := dgStart(t, db, good)
	cfg, err := b.prov.Export(ctx, "dg1")
	is.NoErr(err)
	var kept []config.Connector
	for _, c := range cfg.Connectors {
		if c.Type == "source" {
			kept = append(kept, c)
		}
	}
	cfg.Connectors = kept // drop the destination
	cfg.Status = config.StatusStopped
	err = b.prov.Import(ctx, cfg)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)

	deleted, _ := b.plugins.lifecycleEvents()
	is.Equal(deleted, 0)
	c, err := b.conns.Get(ctx, "dg1:dst") // still there, not replaced by the rollback
	is.NoErr(err)
	is.Equal(c.Config.SettingRefs, map[string]string{"path": dgTemplate})
	// The processors were deleted and re-created by the rollback (new
	// timestamps, no references to lose); the connector records are the bytes
	// the downgrade started from.
	is.Equal(dgConnectorRecords(dgSnapshot(t, db)), before)
}

// S1: after a heal (the reconcile replaced Config) the destination's
// LastActiveConfig still has SettingRefs and a partial Settings. Starting must
// not send that as the "before" of an OnUpdated event.
func TestDowngradeGuard_NoPartialOnUpdatedAfterHeal(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	good := dgWrite(t)
	dgSeed(t, db, good, "connector", "SettingRefs")

	b := dgStart(t, db, good)
	is.NoErr(b.prov.Init(ctx)) // heals Config and starts the pipeline

	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := b.conns.Get(ctx, "dg1:dst")
		is.NoErr(err)
		c.RLock()
		adopted := !c.LastActiveConfig.HasSettingRefs()
		c.RUnlock()
		if adopted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the healed config never became the last active config")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, updated := b.plugins.lifecycleEvents()
	is.Equal(len(updated), 0) // no OnUpdated with an empty "before"
}

// dgConnectorRecords reduces the connector records to what a restore must
// reproduce (Config, LastActiveConfig, State, plugin); UpdatedAt legitimately
// moves.
func dgConnectorRecords(snap map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range snap {
		if !strings.HasPrefix(k, "connector:") {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(v), &rec); err != nil {
			panic(err)
		}
		keep, _ := json.Marshal([]any{rec["Plugin"], rec["Config"], rec["LastActiveConfig"], rec["State"]})
		out[k] = string(keep)
	}
	return out
}

// dgConfigRefs reports whether the Config (not LastActiveConfig) of the
// record under key has SettingRefs.
func dgConfigRefs(t *testing.T, db database.DB, key string) bool {
	t.Helper()
	raw, err := db.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Config, DLQ struct{ SettingRefs map[string]string }
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return len(rec.Config.SettingRefs)+len(rec.DLQ.SettingRefs) > 0
}
