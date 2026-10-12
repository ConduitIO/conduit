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

package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// batchingHarness is a one-source, one-destination arch-v2 service. When built
// with expectOpen=false every plugin dispenser fails the test if anything
// dispenses from it, which is how the refusal tests prove that a refused start
// opened nothing (and so acked nothing: invariant 1 holds trivially).
type batchingHarness struct {
	ls      *Service
	pl      *pipeline.Instance
	source  *connector.Instance
	dest    *connector.Instance
	records []opencdc.Record
}

func newBatchingHarness(
	t *testing.T,
	destSettings, dlqSettings map[string]string,
	expectOpen bool,
) *batchingHarness {
	t.Helper()
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, cancel, persister) })
	ps := pipeline.NewService(logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "batching-test"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	ctrl := gomock.NewController(t)

	h := &batchingHarness{pl: pl, records: generateRecords(10)}
	var sourceDisp, destDisp, dlqDisp connectorPlugin.Dispenser
	var dlq *connector.Instance
	if expectOpen {
		src, sd := oneRecordSource(ctrl, persister, h.records)
		h.source, sourceDisp = src, sd
		dst, dd, _ := sdkBatchingDestination(ctrl, persister, destSettings, 1)
		h.dest, destDisp = dst, dd
		dlq, dlqDisp = asserterDestination(ctrl, persister, nil, false)
	} else {
		h.source, sourceDisp = dummySource(persister), untouchedDispenser(ctrl)
		h.dest, destDisp = dummyDestination(persister), untouchedDispenser(ctrl)
		dlq, dlqDisp = dummyDestination(persister), untouchedDispenser(ctrl)
	}
	h.dest.Config = connector.Config{Name: "batching-destination", Settings: destSettings}
	pl.DLQ.Plugin = dlq.Plugin
	pl.DLQ.Settings = dlqSettings
	_, err = ps.AddConnector(ctx, pl.ID, h.source.ID)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, h.dest.ID)
	is.NoErr(err)

	h.ls = NewService(logger, testErrRecoveryCfg(),
		testConnectorService{h.source.ID: h.source, h.dest.ID: h.dest, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{
			h.source.Plugin: sourceDisp,
			h.dest.Plugin:   destDisp,
			dlq.Plugin:      dlqDisp,
		},
		ps, false)
	return h
}

// requireBatchingRefusal asserts err is the #2979 refusal and returns it.
func requireBatchingRefusal(t *testing.T, err error) *conduiterr.ConduitError {
	t.Helper()
	is := is.New(t)
	is.True(err != nil) // start must be refused
	ce, ok := conduiterr.Get(err)
	is.True(ok) // refusal must be a coded error
	is.Equal(ce.Code, CodeArchV2DestinationBatchingUnsupported)
	is.Equal(CodeArchV2DestinationBatchingUnsupported.Reason(), "pipeline.archv2_destination_batching_unsupported")
	return ce
}

// TestStart_ArchV2RefusesDestinationSDKBatching is the #2979 fix: a pipeline
// whose destination has sdk.batch.size > 1 is refused at Start, with the code,
// the config path, and the fix, before any plugin is dispensed.
func TestStart_ArchV2RefusesDestinationSDKBatching(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"size 100, delay 0 (stalls)":         {"sdk.batch.size": "100", "sdk.batch.delay": "0"},
		"size 100, delay 10ms (>99% slower)": {"sdk.batch.size": "100", "sdk.batch.delay": "10ms"},
		"size 2 is the smallest refused":     {"sdk.batch.size": "2"},
		"padded value":                       {"sdk.batch.size": " 100 "},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, settings, nil, false)

			err := h.ls.Start(context.Background(), h.pl.ID)
			ce := requireBatchingRefusal(t, err)

			is.Equal(ce.ConfigPath, "/connectors/"+h.dest.ID+"/settings/sdk.batch.size")
			is.True(strings.Contains(ce.Message, h.pl.ID))                    // names the pipeline
			is.True(strings.Contains(ce.Message, h.dest.ID))                  // names the destination
			is.True(strings.Contains(ce.Message, `sdk.batch.size`))           // names the setting
			is.True(strings.Contains(ce.Suggestion, "remove sdk.batch.size")) // the fix
			is.True(strings.Contains(ce.Suggestion, "sdk.batch.delay"))
			is.True(strings.Contains(ce.Suggestion, "--preview.pipeline-arch-v2"))
			is.True(ce.Fix != nil)
			is.Equal(ce.Fix.Op, "remove")
			is.Equal(ce.Fix.ConfigPath, ce.ConfigPath)

			// No partial start: nothing running, nothing recorded as a run.
			is.True(h.pl.GetStatus() != pipeline.StatusRunning)
			_, running := h.ls.runningPipelines.Get(h.pl.ID)
			is.True(!running)
		})
	}
}

// TestStart_ArchV2RefusesDLQSDKBatching: the DLQ connector is a destination and
// goes through the same stop-and-wait task, so the same refusal applies, with
// the DLQ's own config path.
func TestStart_ArchV2RefusesDLQSDKBatching(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.size": "50"}, false)

	err := h.ls.Start(context.Background(), h.pl.ID)
	ce := requireBatchingRefusal(t, err)

	is.Equal(ce.ConfigPath, "/dead-letter-queue/settings/sdk.batch.size")
	is.True(strings.Contains(ce.Message, h.pl.ID))
	is.True(strings.Contains(ce.Message, "dead letter queue"))
	is.True(strings.Contains(ce.Suggestion, "remove sdk.batch.size"))
}

// TestStart_ArchV2NamesEveryOffender: with several offenders the message lists
// all of them, so the user fixes the config once.
func TestStart_ArchV2NamesEveryOffender(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, map[string]string{"sdk.batch.size": "10"}, map[string]string{"sdk.batch.size": "20"}, false)

	ce := requireBatchingRefusal(t, h.ls.Start(context.Background(), h.pl.ID))

	is.True(strings.Contains(ce.Message, h.dest.ID))
	is.True(strings.Contains(ce.Message, "dead letter queue"))
	is.Equal(ce.ConfigPath, "/connectors/"+h.dest.ID+"/settings/sdk.batch.size") // first offender
}

// TestStart_ArchV2AllowsNonBatchingDestinations: the defaults, size 0/1,
// negative and unparsable values (left to the plugin's own validation) and a
// delay without a size are all started, and the records flow end to end.
func TestStart_ArchV2AllowsNonBatchingDestinations(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"defaults (no settings)": nil,
		"explicit 0/0":           {"sdk.batch.size": "0", "sdk.batch.delay": "0"},
		"size 1":                 {"sdk.batch.size": "1"},
		"delay only":             {"sdk.batch.delay": "10ms"},
		"unparsable size":        {"sdk.batch.size": "lots"},
		"negative size":          {"sdk.batch.size": "-5"},
		"empty size":             {"sdk.batch.size": ""},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, settings, map[string]string{"sdk.batch.size": "1"}, true)

			is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
			waitForRecordsAcked(t, h.source, h.records)
			is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
			is.NoErr(h.ls.WaitPipeline(h.pl.ID))
		})
	}
}
