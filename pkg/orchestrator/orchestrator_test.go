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

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database/badger"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/orchestrator/mock"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/procbuiltin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// ctxType can be used in tests in call to gomock.AssignableToTypeOf to assert
// a context is passed to a function.
var ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()

func newMockServices(t *testing.T) (*mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *mock.PluginService) {
	ctrl := gomock.NewController(t)

	return mock.NewPipelineService(ctrl),
		mock.NewConnectorService(ctrl),
		mock.NewProcessorService(ctrl),
		mock.NewPluginService(ctrl)
}

func TestPipelineSimple(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)
	logger = logger.CtxHook(ctxutil.MessageIDLogCtxHook{})

	db, err := badger.New(logger.Logger, t.TempDir()+"/test.db")
	is.NoErr(err)
	t.Cleanup(func() {
		err := db.Close()
		is.NoErr(err)
	})

	pluginService := plugin.NewService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories),
		standalone.NewRegistry(logger, ""),
	)

	orc := NewOrchestrator(
		db,
		logger,
		pipeline.NewService(logger, db),
		connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3)),
		processor.NewService(logger, db, processor.GlobalBuilderRegistry),
		pluginService,
	)

	// add builtin processor for removing metadata
	// TODO at the time of writing we don't have a processor for manipulating
	//  metadata, once we have it we can use it instead of adding our own
	processor.GlobalBuilderRegistry.MustRegister("removereadat", func(config processor.Config) (processor.Interface, error) {
		return procbuiltin.NewFuncWrapper(func(ctx context.Context, r record.Record) (record.Record, error) {
			delete(r.Metadata, record.MetadataReadAt) // read at is different every time, remove it
			return r, nil
		}), nil
	})

	// create a host pipeline
	pl, err := orc.Pipelines.Create(ctx, pipeline.Config{Name: "test pipeline"})
	is.NoErr(err)

	// create connectors
	sourcePath := "./fixtures/file-source.txt"
	destinationPath := t.TempDir() + "/destination.txt"
	conn, err := orc.Connectors.Create(
		ctx,
		connector.TypeSource,
		"builtin:file", // use builtin plugin
		pl.ID,
		connector.Config{
			Name:     "test-source",
			Settings: map[string]string{"path": sourcePath},
		},
	)
	is.NoErr(err)

	_, err = orc.Processors.Create(
		ctx,
		"removereadat",
		processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		processor.Config{},
	)
	is.NoErr(err)

	_, err = orc.Connectors.Create(
		ctx,
		connector.TypeDestination,
		"builtin:file", // use builtin plugin
		pl.ID,
		connector.Config{
			Name:     "test-destination",
			Settings: map[string]string{"path": destinationPath},
		},
	)
	is.NoErr(err)

	// start the pipeline now that everything is set up
	err = orc.Pipelines.Start(ctx, pl.ID)
	is.NoErr(err)

	// give the pipeline time to run through
	time.Sleep(time.Second)

	t.Log("stopping pipeline")
	err = orc.Pipelines.Stop(ctx, pl.ID)
	is.NoErr(err)
	t.Log("waiting")
	err = pl.Wait()
	is.NoErr(err)
	t.Log("successfully stopped pipeline")

	want := `{"position":"Mg==","operation":"create","metadata":{"conduit.source.connector.id":"%[1]v","file.path":"./fixtures/file-source.txt","opencdc.version":"v1"},"key":"MQ==","payload":{"before":null,"after":"MQ=="}}
{"position":"NA==","operation":"create","metadata":{"conduit.source.connector.id":"%[1]v","file.path":"./fixtures/file-source.txt","opencdc.version":"v1"},"key":"Mg==","payload":{"before":null,"after":"Mg=="}}
{"position":"Ng==","operation":"create","metadata":{"conduit.source.connector.id":"%[1]v","file.path":"./fixtures/file-source.txt","opencdc.version":"v1"},"key":"Mw==","payload":{"before":null,"after":"Mw=="}}
{"position":"OA==","operation":"create","metadata":{"conduit.source.connector.id":"%[1]v","file.path":"./fixtures/file-source.txt","opencdc.version":"v1"},"key":"NA==","payload":{"before":null,"after":"NA=="}}
{"position":"MTA=","operation":"create","metadata":{"conduit.source.connector.id":"%[1]v","file.path":"./fixtures/file-source.txt","opencdc.version":"v1"},"key":"NQ==","payload":{"before":null,"after":"NQ=="}}
`
	want = fmt.Sprintf(want, conn.ID)

	// make sure destination file matches source file
	got, err := os.ReadFile(destinationPath)
	is.NoErr(err)
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Fatal(diff)
	}
}
