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

package storecompat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/goccy/go-json"
)

// ManifestFile is the name of the file, inside a fixture directory, that
// records which library versions wrote the fixture.
const ManifestFile = "manifest.json"

// SQLiteTable is the table the SQLite fixture uses: Conduit's default
// (pkg/conduit DefaultConfig's DB.SQLite.Table).
const SQLiteTable = "conduit_kv_store"

// Manifest records the build that wrote a fixture. Modules maps a module
// path to the version linked into the generator binary.
type Manifest struct {
	GoVersion string            `json:"goVersion"`
	Modules   map[string]string `json:"modules"`
}

// manifestModules are the modules whose versions decide the on-disk
// format of a fixture.
var manifestModules = []string{
	"github.com/dgraph-io/badger/v4",
	"modernc.org/sqlite",
	"modernc.org/libc",
	"github.com/conduitio/conduit-commons",
}

// CurrentManifest describes the running binary.
func CurrentManifest() Manifest {
	m := Manifest{GoVersion: runtime.Version(), Modules: map[string]string{}}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return m
	}
	for _, dep := range bi.Deps {
		d := dep
		if d.Replace != nil {
			d = d.Replace
		}
		for _, want := range manifestModules {
			if dep.Path == want {
				m.Modules[want] = d.Version
			}
		}
	}
	return m
}

// WriteManifest writes CurrentManifest into dir.
func WriteManifest(dir string) error {
	b, err := json.MarshalIndent(CurrentManifest(), "", "  ")
	if err != nil {
		return cerrors.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, ManifestFile), append(b, '\n'), 0o600)
}

// ReadManifest reads the manifest of the fixture in dir.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return m, cerrors.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, cerrors.Errorf("parse manifest: %w", err)
	}
	return m, nil
}

// IDs and plugin names shared by several fixture instances.
const (
	pipelineID    = "compat-pipeline"
	sourceID      = pipelineID + ":source"
	destinationID = pipelineID + ":destination"
	processorID   = pipelineID + ":proc"
	filePlugin    = "builtin:file"
	pathSetting   = "path"
)

var fixtureTime = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

// Positions are opaque bytes chosen by the connector. These include a NUL
// and non-UTF-8 bytes so that a storage layer that treats values as text
// would corrupt them visibly.
var (
	sourcePosition      = opencdc.Position("lsn:0/16B3748\x00\xff\xfe-offset-41")
	resumedPosition     = opencdc.Position("lsn:0/16B3790\x00\xff\xfe-offset-42")
	destinationPosition = opencdc.Position("lsn:0/16B3700\x00\xff-offset-40")
)

func expectedPipelines() map[string]*pipeline.Instance {
	running := &pipeline.Instance{
		ID: pipelineID,
		Config: pipeline.Config{
			Name:        pipelineID,
			Description: "written by an older storage library",
		},
		CreatedAt:     fixtureTime,
		UpdatedAt:     fixtureTime.Add(time.Minute),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		DLQ: pipeline.DLQ{
			Plugin:              filePlugin,
			Settings:            map[string]string{pathSetting: "/var/lib/conduit/dlq.jsonl"},
			WindowSize:          4,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{sourceID, destinationID},
		ProcessorIDs: []string{processorID},
	}
	running.SetStatus(pipeline.StatusSystemStopped)

	degraded := &pipeline.Instance{
		ID:            "compat-degraded",
		Config:        pipeline.Config{Name: "compat-degraded"},
		Error:         "destination: connection refused",
		CreatedAt:     fixtureTime,
		UpdatedAt:     fixtureTime.Add(2 * time.Minute),
		ProvisionedBy: pipeline.ProvisionTypeAPI,
		DLQ: pipeline.DLQ{
			Plugin:              "builtin:log",
			Settings:            map[string]string{"level": "warn"},
			WindowSize:          1,
			WindowNackThreshold: 0,
		},
	}
	degraded.SetStatus(pipeline.StatusDegraded)

	return map[string]*pipeline.Instance{running.ID: running, degraded.ID: degraded}
}

func expectedConnectors(srcPos opencdc.Position) map[string]*connector.Instance {
	srcCfg := connector.Config{Name: "source", Settings: map[string]string{pathSetting: "/data/in"}}
	dstCfg := connector.Config{Name: "destination", Settings: map[string]string{pathSetting: "/data/out"}}
	src := &connector.Instance{
		ID:               sourceID,
		Type:             connector.TypeSource,
		Config:           srcCfg,
		PipelineID:       pipelineID,
		Plugin:           filePlugin,
		State:            connector.SourceState{Position: srcPos},
		ProvisionedBy:    connector.ProvisionTypeConfig,
		CreatedAt:        fixtureTime,
		UpdatedAt:        fixtureTime.Add(time.Minute),
		LastActiveConfig: srcCfg,
	}
	dst := &connector.Instance{
		ID:         destinationID,
		Type:       connector.TypeDestination,
		Config:     dstCfg,
		PipelineID: pipelineID,
		Plugin:     filePlugin,
		State: connector.DestinationState{Positions: map[string]opencdc.Position{
			sourceID: destinationPosition,
		}},
		ProvisionedBy:    connector.ProvisionTypeConfig,
		CreatedAt:        fixtureTime,
		UpdatedAt:        fixtureTime.Add(time.Minute),
		LastActiveConfig: dstCfg,
	}
	return map[string]*connector.Instance{src.ID: src, dst.ID: dst}
}

func expectedProcessors() map[string]*processor.Instance {
	p := &processor.Instance{
		ID:            processorID,
		CreatedAt:     fixtureTime,
		UpdatedAt:     fixtureTime,
		ProvisionedBy: processor.ProvisionTypeConfig,
		Plugin:        "field.set",
		Condition:     `{{ eq .Metadata.table "orders" }}`,
		Parent:        processor.Parent{ID: pipelineID, Type: processor.ParentTypePipeline},
		Config: processor.Config{
			Settings: map[string]string{"field": ".Payload.After.seen", "value": "true"},
			Workers:  2,
		},
	}
	return map[string]*processor.Instance{p.ID: p}
}

// WriteFixture writes the fixture state into db through the same stores the
// runtime uses. Connector state goes through connector.Persister, which is
// how a running pipeline persists positions (inside a transaction).
func WriteFixture(ctx context.Context, db database.DB) error {
	logger := log.Nop()

	ps := pipeline.NewStore(db)
	for id, p := range expectedPipelines() {
		if err := ps.Set(ctx, id, p); err != nil {
			return cerrors.Errorf("pipeline %q: %w", id, err)
		}
	}
	prs := processor.NewStore(db)
	for id, p := range expectedProcessors() {
		if err := prs.Set(ctx, id, p); err != nil {
			return cerrors.Errorf("processor %q: %w", id, err)
		}
	}
	return persistConnectors(ctx, db, logger, expectedConnectors(sourcePosition))
}

// persistConnectors writes conns through a Persister and waits until the
// flush has committed.
func persistConnectors(ctx context.Context, db database.DB, logger log.CtxLogger, conns map[string]*connector.Instance) error {
	p := connector.NewPersister(logger, db, time.Hour, len(conns)+1)
	p.ConnectorStarted()
	errs := make(chan error, len(conns))
	for _, c := range conns {
		if err := p.Persist(ctx, c, func(err error) { errs <- err }); err != nil {
			return cerrors.Errorf("persist connector %q: %w", c.ID, err)
		}
	}
	p.ConnectorStopped() // triggers the final flush
	p.Wait()
	for range conns {
		if err := <-errs; err != nil {
			return cerrors.Errorf("flush connectors: %w", err)
		}
	}
	return nil
}
