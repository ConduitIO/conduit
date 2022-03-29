// Copyright © 2022 Meroxa, Inc.
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

//go:build integration

package orchestrator

import (
	"context"
	"github.com/rs/zerolog"
	"os"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database/badger"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/go-cmp/cmp"
)

func TestPipelineSimple(t *testing.T) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.InitLogger(zerolog.InfoLevel, "cli")
	logger = logger.CtxHook(ctxutil.MessageIDLogCtxHook{})

	db, err := badger.New(logger.Logger, t.TempDir()+"/test.db")
	assert.Ok(t, err)
	t.Cleanup(func() {
		err := db.Close()
		assert.Ok(t, err)
	})

	pluginService := plugin.NewService(
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories...),
		standalone.NewRegistry(logger),
	)

	orc := NewOrchestrator(
		db,
		pipeline.NewService(logger, db),
		connector.NewService(logger, db, connector.NewDefaultBuilder(logger, connector.NewPersister(logger, db, time.Second, 3), pluginService)),
		processor.NewService(logger, db, processor.GlobalBuilderRegistry),
		pluginService,
	)

	// create a host pipeline
	pl, err := orc.Pipelines.Create(ctx, pipeline.Config{Name: "test pipeline"})
	assert.Ok(t, err)

	// create connectors
	sourcePath := "./fixtures/file-source.txt"
	destinationPath := t.TempDir() + "/destination.txt"
	_, err = orc.Connectors.Create(
		ctx,
		connector.TypeSource,
		connector.Config{
			Name:       "test-source",
			Settings:   map[string]string{"path": sourcePath},
			Plugin:     "builtin:file", // use builtin plugin
			PipelineID: pl.ID,
		},
	)
	assert.Ok(t, err)

	_, err = orc.Connectors.Create(
		ctx,
		connector.TypeDestination,
		connector.Config{
			Name:       "test-destination",
			Settings:   map[string]string{"path": destinationPath},
			Plugin:     "builtin:file", // use builtin plugin
			PipelineID: pl.ID,
		},
	)
	assert.Ok(t, err)

	// start the pipeline now that everything is set up
	err = orc.Pipelines.Start(ctx, pl.ID)
	assert.Ok(t, err)

	// give the pipeline time to run through
	time.Sleep(time.Second)

	t.Log("stopping pipeline")
	err = orc.Pipelines.Stop(ctx, pl.ID)
	assert.Ok(t, err)
	t.Log("waiting")
	err = pl.Wait()
	assert.Ok(t, err)
	t.Log("successfully stopped pipeline")

	// make sure destination file matches source file
	want, err := os.ReadFile(sourcePath)
	assert.Ok(t, err)
	got, err := os.ReadFile(destinationPath)
	assert.Ok(t, err)
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Fatal(diff)
	}
}
