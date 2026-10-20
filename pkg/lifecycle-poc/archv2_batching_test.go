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
	"github.com/conduitio/conduit/pkg/provisioning/config"
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
	opts ...func(*batchingHarness),
) *batchingHarness {
	t.Helper()
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, cancel, persister) })
	ps := pipeline.NewService(logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "batching-test"}, pipeline.ProvisionTypeConfig)
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
	h.dest.LastActiveConfig = h.dest.Config // no lifecycle "created" event on start
	for _, opt := range opts {
		opt(h)
	}
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

// TestStart_ArchV2AllowsDestinationSDKBatching: a destination with
// sdk.batch.size > 1 is no longer refused (#2979 → the funnel batches writes
// itself and strips the setting from the SDK plugin). The records flow end to
// end. batchSize is 2, so the ten records fill five batches and flush during
// the run (not on shutdown), proving the funnel flushes and acks in-flight.
func TestStart_ArchV2AllowsDestinationSDKBatching(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"size 2":             {"sdk.batch.size": "2"},
		"size 2, delay 10ms": {"sdk.batch.size": "2", "sdk.batch.delay": "10ms"},
		"size 5":             {"sdk.batch.size": "5"},
		"padded key/value":   {" sdk.batch.size ": " 5 "},
		"octal 02":           {"sdk.batch.size": "02"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, settings, nil, true)

			is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
			waitForRecordsAcked(t, h.source, h.records)
			is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
			is.NoErr(h.ls.WaitPipeline(h.pl.ID))
		})
	}
}

// TestStart_ArchV2RefusesDLQSDKBatching: the DLQ connector is a destination and
// goes through the same stop-and-wait task, but is not wired through the
// funnel's destination batching, so its SDK batching still withholds acks and
// is refused, with the DLQ's own config path.
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

// TestStart_ArchV2AllowsNonBatchingDestinations: the defaults, size 0/1,
// negative and unparsable values (left to the plugin's own validation) are all
// started, and the records flow end to end.
func TestStart_ArchV2AllowsNonBatchingDestinations(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"defaults (no settings)": nil,
		"explicit 0/0":           {"sdk.batch.size": "0", "sdk.batch.delay": "0"},
		"size 1":                 {"sdk.batch.size": "1"},
		"unparsable size":        {"sdk.batch.size": "lots"},
		"negative size":          {"sdk.batch.size": "-5"},
		"leading zero 01":        {"sdk.batch.size": "01"},
		"hex is rejected by SDK": {"sdk.batch.size": "0x10"},
		"wrong-case key":         {"SDK.BATCH.SIZE": "100"},
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

// TestStart_ArchV2IgnoresSourceBatching: sdk.batch.size on a SOURCE is the
// supported way to batch on arch-v2 and must never be refused or stripped.
func TestStart_ArchV2IgnoresSourceBatching(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, nil, nil, true, func(h *batchingHarness) {
		h.source.Config = connector.Config{Settings: map[string]string{"sdk.batch.size": "100", "sdk.batch.delay": "10ms"}}
		h.source.LastActiveConfig = h.source.Config
	})

	is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
	waitForRecordsAcked(t, h.source, h.records)
	is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
	is.NoErr(h.ls.WaitPipeline(h.pl.ID))
}

// TestPreflightPipelineConfig covers the desired-config check used before a
// live apply: destination batching passes (the funnel batches it), while the
// DLQ is still refused.
func TestPreflightPipelineConfig(t *testing.T) {
	svc := &Service{}
	cfg := func(mut func(*config.Pipeline)) config.Pipeline {
		p := config.Pipeline{
			ID: "p1",
			Connectors: []config.Connector{
				{ID: "p1:src", Type: config.TypeSource, Settings: map[string]string{"sdk.batch.size": "100"}},
				{ID: "p1:out", Type: config.TypeDestination, Name: "Out", Settings: map[string]string{}},
			},
		}
		mut(&p)
		return p
	}

	t.Run("clean config passes, source batching ignored", func(t *testing.T) {
		is := is.New(t)
		is.NoErr(svc.PreflightPipelineConfig(cfg(func(*config.Pipeline) {})))
	})
	t.Run("destination batching passes (batched in the engine)", func(t *testing.T) {
		is := is.New(t)
		is.NoErr(svc.PreflightPipelineConfig(cfg(func(p *config.Pipeline) {
			p.Connectors[1].Settings["sdk.batch.size"] = "50"
		})))
	})
	t.Run("DLQ refused", func(t *testing.T) {
		is := is.New(t)
		err := svc.PreflightPipelineConfig(cfg(func(p *config.Pipeline) {
			p.DLQ.Settings = map[string]string{"sdk.batch.size": "2"}
		}))
		ce := requireBatchingRefusal(t, err)
		is.Equal(ce.ConfigPath, "/dead-letter-queue/settings/sdk.batch.size")
	})
}

// TestStart_ArchV2APIPipelineOmitsConfigPath: an API-created pipeline has no
// config document, so the DLQ refusal carries no ConfigPath and no Fix, but the
// message still names the setting.
func TestStart_ArchV2APIPipelineOmitsConfigPath(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.size": "5"}, false,
		func(h *batchingHarness) { h.pl.ProvisionedBy = pipeline.ProvisionTypeAPI })

	ce := requireBatchingRefusal(t, h.ls.Start(context.Background(), h.pl.ID))

	is.Equal(ce.ConfigPath, "")
	is.True(ce.Fix == nil)
	is.True(strings.Contains(ce.Message, "dead letter queue"))
	is.True(strings.Contains(ce.Message, "sdk.batch.size=5"))
	is.True(!strings.Contains(ce.Message, "/connectors/")) // no pointer into a document that does not exist
	is.True(strings.Contains(ce.Suggestion, "remove sdk.batch.size"))
}

// TestDLQSettingPointer_EscapesKey pins RFC 6901 escaping of the DLQ key
// segment. Removing jsonPointerEscape from dlqSettingPointer fails it.
func TestDLQSettingPointer_EscapesKey(t *testing.T) {
	is := is.New(t)
	is.Equal(dlqSettingPointer("a/b~c"), "/dead-letter-queue/settings/a~1b~0c")
}

// TestPreflightPipelineConfig_MixedPaddedAndPlainKeys: see the Start variant.
func TestPreflightPipelineConfig_MixedPaddedAndPlainKeys(t *testing.T) {
	is := is.New(t)
	svc := &Service{}
	//nolint:gocritic // the padded key is the point
	mixed := map[string]string{" sdk.batch.size": "1", "sdk.batch.size": "100"}
	err := svc.PreflightPipelineConfig(config.Pipeline{
		ID: "p1",
		Connectors: []config.Connector{
			{ID: "p1:out", Type: config.TypeDestination, Settings: mixed},
		},
	})
	is.NoErr(err) // destination batching is no longer refused
}
