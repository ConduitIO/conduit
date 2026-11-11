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
	"bytes"
	"context"
	"io"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	commonsconfig "github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	condprom "github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/google/uuid"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// specDispenser attaches a plugin specification to a test dispenser, as the
// built-in and standalone registries do for the dispensers they build (see
// connectorPlugin.DispenserSpecification).
type specDispenser struct {
	connectorPlugin.Dispenser
	spec pconnector.Specification
}

func (d specDispenser) Specification() (pconnector.Specification, bool) { return d.spec, true }

// s3ShapedParams is the destination spec of a connector that enables SDK
// batching by default, like conduit-connector-s3 v0.12.0/v0.13.0
// (connector.yaml: sdk.batch.size "1000", sdk.batch.delay "1s").
func s3ShapedParams() commonsconfig.Parameters {
	return commonsconfig.Parameters{
		config.SDKBatchSizeSetting: {
			Default:     "1000",
			Type:        commonsconfig.ParameterTypeInt,
			Validations: []commonsconfig.Validation{commonsconfig.ValidationGreaterThan{V: -1}},
		},
		config.SDKBatchDelaySetting: {Default: "1s", Type: commonsconfig.ParameterTypeDuration},
	}
}

// s3ShapedConfig is the SDK batch middleware alone, set to S3's code default
// before the SDK parses the settings, as S3 does.
type s3ShapedConfig struct {
	sdk.UnimplementedDestinationConfig
	sdk.DestinationWithBatch
}

// s3ShapedDestination is a conduit-connector-sdk destination (the real SDK
// adapter, middleware and batcher) whose batching defaults are S3's. It records
// every Write and the settings Configure received.
type s3ShapedDestination struct {
	sdk.UnimplementedDestination
	cfg s3ShapedConfig

	mu         sync.Mutex
	configured map[string]string
	writes     []int
	written    int
}

func newS3ShapedDestination() *s3ShapedDestination {
	d := &s3ShapedDestination{}
	d.cfg.BatchSize = 1000
	d.cfg.BatchDelay = time.Second
	return d
}

func (d *s3ShapedDestination) Config() sdk.DestinationConfig  { return &d.cfg }
func (d *s3ShapedDestination) Open(context.Context) error     { return nil }
func (d *s3ShapedDestination) Teardown(context.Context) error { return nil }

func (d *s3ShapedDestination) Write(_ context.Context, recs []opencdc.Record) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, len(recs))
	d.written += len(recs)
	return len(recs), nil
}

func (d *s3ShapedDestination) snapshot() (configured map[string]string, writes []int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.configured), append([]int(nil), d.writes...)
}

// inProcessDestination runs the SDK's destination plugin adapter in process
// over an in-memory stream, recording the settings the engine hands its
// Configure. It stands in for the built-in adapter, whose sandbox pools
// channels across goroutines and so cannot run in a synctest bubble.
type inProcessDestination struct {
	pconnector.DestinationPlugin
	dest *s3ShapedDestination
}

func (p *inProcessDestination) NewStream() pconnector.DestinationRunStream {
	return &builtin.InMemoryDestinationRunStream{}
}

func (p *inProcessDestination) Configure(ctx context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	p.dest.mu.Lock()
	p.dest.configured = maps.Clone(req.Config)
	p.dest.mu.Unlock()
	return p.DestinationPlugin.Configure(ctx, req)
}

func (p *inProcessDestination) Run(ctx context.Context, stream pconnector.DestinationRunStream) error {
	s := stream.(*builtin.InMemoryDestinationRunStream)
	s.Init(ctx)
	go func() {
		if err := p.DestinationPlugin.Run(ctx, stream); err != nil {
			s.Close(err)
			return
		}
		s.Close(io.EOF)
	}()
	return nil
}

// s3ShapedDispenserT dispenses dest through the SDK adapter, with the
// S3-shaped spec attached as the plugin registries attach it.
type s3ShapedDispenserT struct {
	dest   *s3ShapedDestination
	params commonsconfig.Parameters
}

func (d s3ShapedDispenserT) Specification() (pconnector.Specification, bool) {
	return pconnector.Specification{Name: "s3-shaped", DestinationParams: d.params}, true
}

func (d s3ShapedDispenserT) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("not a specifier")
}

func (d s3ShapedDispenserT) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	return nil, cerrors.New("not a source")
}

func (d s3ShapedDispenserT) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return &inProcessDestination{
		DestinationPlugin: sdk.NewDestinationPlugin(sdk.DestinationWithMiddleware(d.dest), pconnector.PluginConfig{}, d.params),
		dest:              d.dest,
	}, nil
}

func s3ShapedDispenser(dest *s3ShapedDestination) connectorPlugin.Dispenser {
	return s3ShapedDispenserT{dest: dest, params: s3ShapedParams()}
}

// ownershipHarness is a one-source, one-destination arch-v2 pipeline whose
// destination is s3ShapedDestination and whose source emits one record per
// read and then goes idle.
type ownershipHarness struct {
	ls      *Service
	pl      *pipeline.Instance
	source  *connector.Instance
	dest    *s3ShapedDestination
	records []opencdc.Record
}

func newOwnershipHarness(t *testing.T, destSettings map[string]string, records int) *ownershipHarness {
	t.Helper()
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, cancel, persister) })
	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "ownership-test"}, pipeline.ProvisionTypeConfig)
	is.NoErr(err)
	ctrl := gomock.NewController(t)

	h := &ownershipHarness{pl: pl, dest: newS3ShapedDestination(), records: generateRecords(records)}
	src, srcDisp := oneRecordSource(ctrl, persister, h.records)
	h.source = src
	destInstance := dummyDestination(persister)
	destInstance.Config = connector.Config{Name: "s3-shaped", Settings: destSettings}
	destInstance.LastActiveConfig = destInstance.Config
	dlq, dlqDisp := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin
	_, err = ps.AddConnector(ctx, pl.ID, src.ID)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, destInstance.ID)
	is.NoErr(err)

	h.ls = NewService(logger, testErrRecoveryCfg(),
		testConnectorService{src.ID: src, destInstance.ID: destInstance, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{
			src.Plugin:          srcDisp,
			destInstance.Plugin: s3ShapedDispenser(h.dest),
			dlq.Plugin:          dlqDisp,
		},
		ps, false)
	return h
}

// start starts the pipeline and makes sure it is stopped before the bubble
// ends, also when the test fails early, so a failure is reported as one
// instead of as a synctest deadlock.
func (h *ownershipHarness) start(t *testing.T) {
	t.Helper()
	is := is.New(t)
	is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
	t.Cleanup(func() {
		_ = h.ls.Stop(context.Background(), h.pl.ID, false)
		_ = h.ls.WaitPipeline(h.pl.ID)
	})
}

// ackedAll reports whether the source has acked its last record.
func (h *ownershipHarness) ackedAll() bool {
	want := h.records[len(h.records)-1].Position
	h.source.RLock()
	defer h.source.RUnlock()
	state, ok := h.source.State.(connector.SourceState)
	return ok && bytes.Equal(state.Position, want)
}

// waitAckedWithin polls (in the bubble's fake time) until every record is
// acked, failing if that takes longer than bound. It returns the fake time it
// took.
func (h *ownershipHarness) waitAckedWithin(t *testing.T, bound time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for !h.ackedAll() {
		if time.Since(start) > bound {
			_, writes := h.dest.snapshot()
			t.Fatalf("not every record was acked within %v of fake time (plugin writes so far: %v)", bound, writes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start)
}

// TestArchV2_SpecDefaultBatching_TimeToAck (#3321): a destination whose
// connector batches by default (S3: 1000 / 1 s) behind a source that reads one
// record at a time, with no sdk.batch.* in the pipeline. On main the engine did
// not batch (the pipeline config set nothing), the plugin's SDK batcher held
// each ack for its 1 s delay, and the stop-and-wait loop read the next record
// only after it: one record per second. With the fix the engine adopts the
// spec default, batches the records itself, and configures the plugin "0"/"0",
// so every record is written in one engine flush at the 1 s deadline and acked
// at once.
//
// It runs in a synctest bubble, so the 1 s delays are fake time.
func TestArchV2_SpecDefaultBatching_TimeToAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		const records = 30
		h := newOwnershipHarness(t, nil, records)

		h.start(t)
		took := h.waitAckedWithin(t, 5*time.Second) // main: about 30 s of fake time

		configured, writes := h.dest.snapshot()
		is.Equal(configured, map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"}) // plugin batcher off
		is.Equal(writes, []int{records})                                                       // one engine flush of 1000-size batch
		is.True(took >= time.Second)                                                           // taken by the adopted 1 s delay
		is.True(took < 2*time.Second)

		is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
		is.NoErr(h.ls.WaitPipeline(h.pl.ID))
	})
}

// TestArchV2_PipelineBatching_PluginDefaultDoesNotComeBack (double batching,
// design 20261103, Decision 2): the pipeline sets sdk.batch.* on an S3-shaped
// destination. On main the engine batched and deleted both keys before
// Configure, which brought S3's own 1000 / 1 s default back, so the plugin
// batched behind the engine: an engine flush smaller than 1,000 records (any
// partial batch, including the timer's) waited a further second inside the
// plugin for its acks. With the fix the plugin gets "0"/"0" and acks the flush
// as soon as it is written.
func TestArchV2_PipelineBatching_PluginDefaultDoesNotComeBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		const records = 500
		h := newOwnershipHarness(t, map[string]string{"sdk.batch.size": "10000", "sdk.batch.delay": "100ms"}, records)

		h.start(t)
		// The engine timer takes the 500 records at 100 ms; the plugin must
		// not add its own 1 s on top (main: about 1.1 s).
		took := h.waitAckedWithin(t, 500*time.Millisecond)
		is.True(took >= 100*time.Millisecond)

		configured, writes := h.dest.snapshot()
		is.Equal(configured, map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"})
		is.Equal(writes, []int{records})

		is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
		is.NoErr(h.ls.WaitPipeline(h.pl.ID))
	})
}

// TestArchV2_OwnershipLogsAndCounts: a destination batched from its connector
// default logs the INFO line with the origin of each value; one with nothing
// to neutralise logs nothing. Every destination whose plugin batching was
// neutralised is counted once in
// conduit_funnel_destination_plugin_batching_neutralised_total, read back
// through a metrics registry (removing the engine's increment fails it).
func TestArchV2_OwnershipLogsAndCounts(t *testing.T) {
	for name, tc := range map[string]struct {
		settings  map[string]string
		wantLog   bool
		wantCount float64
	}{
		"spec default":       {settings: nil, wantLog: true, wantCount: 1},
		"pipeline size only": {settings: map[string]string{"sdk.batch.size": "500"}, wantLog: true, wantCount: 1}, // delay from the spec
		"pipeline sets both": {settings: map[string]string{"sdk.batch.size": "500", "sdk.batch.delay": "200ms"}, wantCount: 1},
		"pipeline turns off": {settings: map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"}},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			// Registered before anything calls WithValues: a registry only
			// sees the values recorded after it is attached.
			promReg := registerMetricsBackend(t)
			var destID string
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				h := newOwnershipHarness(t, tc.settings, 5)
				destID = h.pl.ConnectorIDs[1]
				logs := &syncBuffer{}
				h.ls.logger = log.New(zerolog.New(logs).Level(zerolog.InfoLevel))

				h.start(t)
				h.waitAckedWithin(t, 5*time.Second)
				is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
				is.NoErr(h.ls.WaitPipeline(h.pl.ID))

				got := strings.Count(logs.String(), "destination batching taken over from connector defaults")
				if tc.wantLog {
					is.Equal(got, 1)
					is.True(strings.Contains(logs.String(), `"sdk_batch_delay_origin":"connector_default"`))
				} else {
					is.Equal(got, 0)
				}
			})
			is.Equal(neutralisedCount(t, promReg, destID), tc.wantCount)
		})
	}
}

// registerMetricsBackend attaches a fresh metrics backend to the global
// measure specs, as pkg/conduit/runtime.go does at startup, and returns a
// Prometheus registry that scrapes only it.
func registerMetricsBackend(t *testing.T) *promclient.Registry {
	t.Helper()
	reg := condprom.NewRegistry(nil)
	metrics.Register(reg)
	promReg := promclient.NewRegistry()
	if err := promReg.Register(reg); err != nil {
		t.Fatalf("register metrics backend: %v", err)
	}
	return promReg
}

// neutralisedCount returns the value of the neutralised-plugin-batching
// counter for the destination componentID (0 when it was never incremented).
func neutralisedCount(t *testing.T, promReg *promclient.Registry, componentID string) float64 {
	t.Helper()
	families, err := promReg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "conduit_funnel_destination_plugin_batching_neutralised_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "component_id" && l.GetValue() == componentID {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestResolveDestinationBatching is the table of the per-key resolution and
// the override rule (design 20261103, Decision 2).
func TestResolveDestinationBatching(t *testing.T) {
	s3 := s3ShapedParams()
	off := commonsconfig.Parameters{
		config.SDKBatchSizeSetting:  {Default: "0", Type: commonsconfig.ParameterTypeInt},
		config.SDKBatchDelaySetting: {Default: "0", Type: commonsconfig.ParameterTypeDuration},
	}
	sizeOnlySpec := commonsconfig.Parameters{config.SDKBatchSizeSetting: {Default: "1000"}}
	tests := []struct {
		name      string
		settings  map[string]string
		params    commonsconfig.Parameters
		noSpec    bool
		wantSize  int
		wantDelay time.Duration
		wantSO    batchOrigin
		wantDO    batchOrigin
		wantOver  []string
	}{
		{
			name: "config only, spec default off", params: off,
			settings: map[string]string{"sdk.batch.size": "100", "sdk.batch.delay": "10ms"},
			wantSize: 100, wantDelay: 10 * time.Millisecond, wantSO: batchOriginPipeline, wantDO: batchOriginPipeline,
			wantOver: []string{"sdk.batch.size", "sdk.batch.delay"},
		},
		{
			name: "spec only", params: s3,
			wantSize: 1000, wantDelay: time.Second, wantSO: batchOriginConnector, wantDO: batchOriginConnector,
			wantOver: []string{"sdk.batch.size", "sdk.batch.delay"},
		},
		{
			name: "per-key merge", params: s3, settings: map[string]string{"sdk.batch.size": "10000"},
			wantSize: 10000, wantDelay: time.Second, wantSO: batchOriginPipeline, wantDO: batchOriginConnector,
			wantOver: []string{"sdk.batch.size", "sdk.batch.delay"},
		},
		{
			name: "empty value takes the spec default, as the SDK does", params: s3,
			settings: map[string]string{"sdk.batch.size": " ", "sdk.batch.delay": ""},
			wantSize: 1000, wantDelay: time.Second, wantSO: batchOriginConnector, wantDO: batchOriginConnector,
			wantOver: []string{"sdk.batch.size", "sdk.batch.delay"},
		},
		{
			name: "unparsable value passed through, other key still neutralised", params: s3,
			settings: map[string]string{"sdk.batch.size": "lots"},
			wantSize: 1000, wantDelay: time.Second, wantSO: batchOriginConnector, wantDO: batchOriginConnector,
			wantOver: []string{"sdk.batch.delay"},
		},
		{
			name: "negative value passed through", params: s3,
			settings: map[string]string{"sdk.batch.size": "-5", "sdk.batch.delay": "-1s"},
			wantSO:   batchOriginPipeline, wantDO: batchOriginPipeline,
		},
		{
			name: "padded key matched once", params: s3,
			//nolint:gocritic // the padded key is the point
			settings: map[string]string{" sdk.batch.size ": " 10 ", "sdk.batch.delay": "0"},
			wantSize: 10, wantSO: batchOriginPipeline, wantDO: batchOriginPipeline,
			wantOver: []string{"sdk.batch.size"},
		},
		{
			name: "spec without the keys gets nothing", params: commonsconfig.Parameters{},
			settings: map[string]string{"sdk.batch.size": "100"},
			wantSize: 100, wantSO: batchOriginPipeline, wantDO: batchOriginNone,
		},
		{
			name: "spec default off gets nothing", params: off,
			wantSO: batchOriginNone, wantDO: batchOriginNone,
		},
		{
			name: "explicit 0/0 disables both", params: s3,
			settings: map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"},
			wantSO:   batchOriginPipeline, wantDO: batchOriginPipeline,
		},
		{
			name: "size 1 does not batch", params: off, settings: map[string]string{"sdk.batch.size": "1"},
			wantSize: 1, wantSO: batchOriginPipeline, wantDO: batchOriginNone,
		},
		{
			name: "spec declares size only", params: sizeOnlySpec,
			wantSize: 1000, wantSO: batchOriginConnector, wantDO: batchOriginNone,
			wantOver: []string{"sdk.batch.size"},
		},
		{
			name: "no spec: pipeline-set enabling keys are replaced, nothing added", noSpec: true,
			settings: map[string]string{"sdk.batch.size": "100"},
			wantSize: 100, wantSO: batchOriginPipeline, wantDO: batchOriginNone,
			wantOver: []string{"sdk.batch.size"},
		},
		{
			name: "no spec and no settings", noSpec: true,
			wantSO: batchOriginNone, wantDO: batchOriginNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := resolveDestinationBatching(tc.settings, tc.params, !tc.noSpec)
			is.Equal(got.size, tc.wantSize)
			is.Equal(got.delay, tc.wantDelay)
			is.Equal(got.sizeOrigin, tc.wantSO)
			is.Equal(got.delayOrigin, tc.wantDO)
			is.Equal(got.overrides, tc.wantOver)
		})
	}
}

// TestBuildDLQ_NeutralisesSpecDefaultBatching (#3321): a DLQ connector whose
// spec batches by default (S3 as a DLQ) would hold each nack's ack for its
// 1 s delay. The DLQ is not engine-batched, so buildDLQ configures the plugin
// "0"/"0" and every nack is written synchronously. On main nothing reached
// Configure for these keys and the plugin batched.
func TestBuildDLQ_NeutralisesSpecDefaultBatching(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())
	persister := connector.NewPersister(logger, &inmemory.DB{}, time.Second, 3)
	dest := newS3ShapedDestination()
	dlqInstance := dummyDestination(persister)
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "dlq-test"},
		DLQ:    pipeline.DLQ{Plugin: dlqInstance.Plugin, WindowSize: 1, WindowNackThreshold: 0},
	}
	ls := NewService(logger, testErrRecoveryCfg(),
		testConnectorService{testDLQID: dlqInstance},
		testProcessorService{},
		testConnectorPluginService{dlqInstance.Plugin: s3ShapedDispenser(dest)},
		testPipelineService{}, false)

	dlq, err := ls.buildDLQ(ctx, pl, "source-1", logger)
	is.NoErr(err)
	is.NoErr(dlq.Open(ctx))
	t.Cleanup(func() { is.NoErr(dlq.Close(ctx)) })

	configured, _ := dest.snapshot()
	is.Equal(configured, map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"})
}

// TestBatchingGuard_RefusesDLQDelay (#3321): the SDK batches a destination on
// sdk.batch.delay alone, with a size of its own, so an explicit DLQ delay
// above 0 withholds acks like a size above 1 does. Start refuses it before
// anything is opened, and so does the live-apply preflight. On main both let
// it through.
func TestBatchingGuard_RefusesDLQDelay(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		is := is.New(t)
		h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.delay": "1s"}, false)
		ce := requireBatchingRefusal(t, h.ls.Start(context.Background(), h.pl.ID))
		is.Equal(ce.ConfigPath, "/dead-letter-queue/settings/sdk.batch.delay")
		is.True(strings.Contains(ce.Message, "sdk.batch.delay=1s"))
		is.Equal(ce.Fix.Op, "remove")
	})
	t.Run("start, API pipeline names the setting", func(t *testing.T) {
		is := is.New(t)
		h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.delay": "1s"}, false,
			func(h *batchingHarness) { h.pl.ProvisionedBy = pipeline.ProvisionTypeAPI })
		ce := requireBatchingRefusal(t, h.ls.Start(context.Background(), h.pl.ID))
		is.Equal(ce.ConfigPath, "")
		is.True(strings.Contains(ce.Message, "sdk.batch.delay=1s"))
	})

	svc := &Service{}
	for name, tc := range map[string]struct {
		settings map[string]string
		refused  bool
		path     string
	}{
		"delay 1s":        {settings: map[string]string{"sdk.batch.delay": "1s"}, refused: true, path: "/dead-letter-queue/settings/sdk.batch.delay"},
		"delay 1ns":       {settings: map[string]string{"sdk.batch.delay": "1ns"}, refused: true, path: "/dead-letter-queue/settings/sdk.batch.delay"}, // any delay above 0, not above some threshold
		"padded delay":    {settings: map[string]string{" sdk.batch.delay": " 10ms "}, refused: true, path: dlqSettingPointer(" sdk.batch.delay")},
		"size and delay":  {settings: map[string]string{"sdk.batch.size": "5", "sdk.batch.delay": "1s"}, refused: true, path: "/dead-letter-queue/settings/sdk.batch.size"},
		"delay 0":         {settings: map[string]string{"sdk.batch.delay": "0"}},
		"delay 0s":        {settings: map[string]string{"sdk.batch.delay": "0s"}},
		"negative delay":  {settings: map[string]string{"sdk.batch.delay": "-1s"}},
		"unparsable":      {settings: map[string]string{"sdk.batch.delay": "soon"}},
		"no DLQ settings": {},
	} {
		t.Run("preflight "+name, func(t *testing.T) {
			is := is.New(t)
			err := svc.PreflightPipelineConfig(config.Pipeline{ID: "p1", DLQ: config.DLQ{Settings: tc.settings}})
			if !tc.refused {
				is.NoErr(err)
				return
			}
			ce := requireBatchingRefusal(t, err)
			is.Equal(ce.ConfigPath, tc.path)
		})
	}
}

// errStopHere ends a test's Open after Configure.
var errStopHere = cerrors.New("stop here")

// TestArchV2_DestinationSettingsVerbatimWithoutBatching: a destination that
// does not batch (spec default off, nothing in the pipeline) gets its settings
// verbatim, with no injected key: a connector whose spec forbids "0" keeps
// working as before.
func TestArchV2_DestinationSettingsVerbatimWithoutBatching(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())
	persister := connector.NewPersister(logger, &inmemory.DB{}, time.Second, 3)
	ctrl := gomock.NewController(t)

	var got map[string]string
	plugin := pmock.NewDestinationPlugin(ctrl)
	plugin.EXPECT().Configure(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
			got = maps.Clone(req.Config)
			return pconnector.DestinationConfigureResponse{}, nil
		})
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)
	plugin.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.DestinationOpenResponse{}, errStopHere)
	disp := pmock.NewDispenser(ctrl)
	disp.EXPECT().DispenseDestination().Return(plugin, nil)

	dest := dummyDestination(persister)
	dest.Config = connector.Config{Settings: map[string]string{"foo": "bar"}}
	dest.LastActiveConfig = dest.Config
	pl := &pipeline.Instance{ID: uuid.NewString(), Config: pipeline.Config{Name: "p"}, ConnectorIDs: []string{dest.ID}}
	ls := NewService(logger, testErrRecoveryCfg(),
		testConnectorService{dest.ID: dest},
		testProcessorService{},
		testConnectorPluginService{dest.Plugin: specDispenser{Dispenser: disp, spec: pconnector.Specification{
			DestinationParams: commonsconfig.Parameters{
				config.SDKBatchSizeSetting:  {Default: "1", Validations: []commonsconfig.Validation{commonsconfig.ValidationGreaterThan{V: 0}}},
				config.SDKBatchDelaySetting: {Default: ""},
			},
		}}},
		testPipelineService{}, false)

	tasks, err := ls.buildDestinationTasks(ctx, pl, logger)
	is.NoErr(err)
	is.Equal(len(tasks), 1)
	destTask := tasks[0][len(tasks[0])-1]
	is.True(cerrors.Is(destTask.Open(ctx), errStopHere))
	is.Equal(got, map[string]string{"foo": "bar"})
}

// TestArchV2_RefusesNonLiteralBatching (#3321 review, Medium 1): the engine
// resolves batching from the stored settings, and the connector's settings
// resolver expands ${VAR} only for the plugin. With S3_BATCH=10000 allowlisted
// and sdk.batch.size: ${S3_BATCH} on an S3-shaped destination, the engine
// would batch 1000 / 1 s (the spec default) and configure only the delay "0",
// so the plugin would run a 10000-record size-only batcher and hold the acks
// of every engine flush until records the engine never sends fill it: a stall
// ("mechanism" pins that arithmetic). With sdk.batch.delay: $S3_DELAY (5s) the
// plugin would run a delay-only batcher behind the engine and every flush
// would wait 5 s more (double batching). Start now refuses both before
// anything is dispensed: every dispenser here fails the test if it is used, so
// without the guard the test fails on the first dispense.
func TestArchV2_RefusesNonLiteralBatching(t *testing.T) {
	allow, err := env.NewAllowlist([]string{"S3_BATCH", "S3_DELAY"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("S3_BATCH", "10000")
	t.Setenv("S3_DELAY", "5s")
	resolver := connector.EnvSettingsResolver{Allow: allow}

	t.Run("mechanism", func(t *testing.T) {
		is := is.New(t)
		raw := map[string]string{"sdk.batch.size": "${S3_BATCH}"}
		engine := resolveDestinationBatching(raw, s3ShapedParams(), true)
		is.Equal(engine.size, 1000)         // the reference reads as unset: spec default
		is.Equal(engine.delay, time.Second) // engine flushes at most 1000 records, after 1 s
		is.Equal(engine.overrides, []string{config.SDKBatchDelaySetting})

		plugin, err := resolver.ResolveSettings("s3-shaped", raw)
		is.NoErr(err)
		plugin[config.SDKBatchDelaySetting] = "0" // the override the engine would apply
		size, delay, enabled := config.SDKBatchSettings(plugin)
		is.True(enabled)
		is.Equal(size, 10000) // the plugin waits for 10000 records the engine never sends at once
		is.Equal(delay, time.Duration(0))
	})

	for name, tc := range map[string]struct {
		settings map[string]string
		path     string
		inMsg    string
	}{
		"size reference (stall)": {
			settings: map[string]string{"sdk.batch.size": "${S3_BATCH}"},
			path:     "/connectors/1/settings/sdk.batch.size",
			inMsg:    "sdk.batch.size=${S3_BATCH}",
		},
		"delay reference (double batching)": {
			settings: map[string]string{"sdk.batch.size": "500", "sdk.batch.delay": "$S3_DELAY"},
			path:     "/connectors/1/settings/sdk.batch.delay",
			inMsg:    "sdk.batch.delay=$S3_DELAY",
		},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, tc.settings, nil, false, func(h *batchingHarness) {
				// Built by a connector.Service with the env resolver, as the
				// runtime builds it, so a dispense would get ${S3_BATCH}
				// expanded.
				d, err := connector.NewService(log.Nop(), &inmemory.DB{}, nil, connector.WithSettingsResolver(resolver)).Create(
					context.Background(), h.dest.ID, connector.TypeDestination, h.dest.Plugin, h.pl.ID, h.dest.Config, connector.ProvisionTypeConfig)
				is.NoErr(err)
				d.LastActiveConfig = d.Config
				h.dest = d
			})

			ce, ok := conduiterr.Get(h.ls.Start(context.Background(), h.pl.ID))
			is.True(ok) // the refusal is a coded error
			is.Equal(ce.Code, CodeArchV2DestinationBatchingNotLiteral)
			is.Equal(ce.Code.Reason(), "pipeline.archv2_destination_batching_not_literal")
			is.Equal(ce.ConfigPath, tc.path)
			is.True(strings.Contains(ce.Message, tc.inMsg))     // names the setting as written
			is.True(!strings.Contains(ce.Message, "10000"))     // never the expansion
			is.True(strings.Contains(ce.Suggestion, "literal")) // says how to fix it
			is.True(ce.Fix == nil)
		})
	}
}

// TestBatchingGuard_NonLiteral covers the shared predicate on every surface the
// engine owns: Start for an API-created pipeline (no config document, so no
// ConfigPath) and for a DLQ reference that the numeric DLQ refusal cannot read,
// and the live-apply preflight for destinations and the DLQ.
func TestBatchingGuard_NonLiteral(t *testing.T) {
	t.Run("start, DLQ reference", func(t *testing.T) {
		is := is.New(t)
		h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.delay": "${DLQ_DELAY}"}, false)
		ce, ok := conduiterr.Get(h.ls.Start(context.Background(), h.pl.ID))
		is.True(ok)
		is.Equal(ce.Code, CodeArchV2DestinationBatchingNotLiteral)
		is.Equal(ce.ConfigPath, "/dead-letter-queue/settings/sdk.batch.delay")
		is.True(strings.Contains(ce.Message, "the dead letter queue sets sdk.batch.delay=${DLQ_DELAY}"))
	})
	t.Run("start, API pipeline names the setting", func(t *testing.T) {
		is := is.New(t)
		h := newBatchingHarness(t, map[string]string{" sdk.batch.size": "$N"}, nil, false,
			func(h *batchingHarness) { h.pl.ProvisionedBy = pipeline.ProvisionTypeAPI })
		ce, ok := conduiterr.Get(h.ls.Start(context.Background(), h.pl.ID))
		is.True(ok)
		is.Equal(ce.Code, CodeArchV2DestinationBatchingNotLiteral)
		is.Equal(ce.ConfigPath, "")
		is.True(strings.Contains(ce.Message, h.dest.ID))
		is.True(strings.Contains(ce.Message, "sdk.batch.size=$N"))
	})

	svc := &Service{}
	dest := func(settings map[string]string) config.Connector {
		return config.Connector{ID: "dest", Type: config.TypeDestination, Settings: settings}
	}
	for name, tc := range map[string]struct {
		cfg  config.Pipeline
		path string // empty: not refused
	}{
		"destination size reference": {
			cfg:  config.Pipeline{Connectors: []config.Connector{{ID: "src", Type: config.TypeSource}, dest(map[string]string{"sdk.batch.size": "${S3_BATCH}"})}},
			path: "/connectors/1/settings/sdk.batch.size",
		},
		"destination delay, padded key": {
			cfg:  config.Pipeline{Connectors: []config.Connector{dest(map[string]string{"sdk.batch.delay ": "${D}s"})}},
			path: "/connectors/0/settings/sdk.batch.delay ",
		},
		"escaped dollar": {
			cfg:  config.Pipeline{Connectors: []config.Connector{dest(map[string]string{"sdk.batch.size": "$$1"})}},
			path: "/connectors/0/settings/sdk.batch.size",
		},
		"DLQ size reference": {
			cfg:  config.Pipeline{DLQ: config.DLQ{Settings: map[string]string{"sdk.batch.size": "${N}"}}},
			path: "/dead-letter-queue/settings/sdk.batch.size",
		},
		"source reference is not batching the engine owns": {
			cfg: config.Pipeline{Connectors: []config.Connector{{ID: "src", Type: config.TypeSource, Settings: map[string]string{"sdk.batch.size": "${N}"}}}},
		},
		"reference in another destination setting": {
			cfg: config.Pipeline{Connectors: []config.Connector{dest(map[string]string{"url": "${URL}", "sdk.batch.size": "100"})}},
		},
		"literal values": {
			cfg: config.Pipeline{Connectors: []config.Connector{dest(map[string]string{"sdk.batch.size": "100", "sdk.batch.delay": "1s"})}},
		},
	} {
		t.Run("preflight "+name, func(t *testing.T) {
			is := is.New(t)
			tc.cfg.ID = "p1"
			err := svc.PreflightPipelineConfig(tc.cfg)
			if tc.path == "" {
				is.NoErr(err)
				return
			}
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeArchV2DestinationBatchingNotLiteral)
			is.Equal(ce.ConfigPath, tc.path)
		})
	}
}
