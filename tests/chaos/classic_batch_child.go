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

package chaos

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/rs/zerolog"
)

// This file is the child side of the classic default destination batching
// SIGKILL scenario (classic_batch_sigkill_test.go,
// docs/design-documents/20261020-classic-default-destination-batching.md).
//
// It drives the classic engine's connector.Destination exactly as
// pkg/lifecycle does (SetClassicDefaultBatching, then Open), over a REAL
// conduit-connector-sdk destination adapter (so the SDK's batch write strategy
// is the code under test) wrapped in a REAL builtin.Dispenser named
// "builtin:postgres" (so the allowlist applies). Only the connector behind the
// SDK is fake: its Write fsyncs every record into a deliveryLog before it
// returns, which is the stand-in for "durably written to the database".
const (
	envClassicBatchMode     = "CONDUIT_CHAOS_CLASSIC_BATCH_MODE"
	envClassicBatchLogDir   = "CONDUIT_CHAOS_CLASSIC_BATCH_LOG_DIR"
	envClassicBatchFrom     = "CONDUIT_CHAOS_CLASSIC_BATCH_FROM"
	envClassicBatchTotal    = "CONDUIT_CHAOS_CLASSIC_BATCH_TOTAL"
	envClassicBatchPaceMS   = "CONDUIT_CHAOS_CLASSIC_BATCH_PACE_MS"
	envClassicBatchSettings = "CONDUIT_CHAOS_CLASSIC_BATCH_SETTINGS" // k=v,k=v: the user's destination settings
	envClassicBatchBlock    = "CONDUIT_CHAOS_CLASSIC_BATCH_BLOCK"    // the first Write blocks forever, before anything is durable

	markerClassicWrite = "WRITE " // "WRITE <n>": the connector's Write received n records
	markerClassicAcked = "ACKED " // "ACKED <pos>": the engine saw the ack for pos
	markerClassicDone  = "CLASSIC_BATCH_DONE"
	markerClassicBlock = "WRITE_BLOCKED "
)

// classicBatchChildConfig is the parent-side counterpart of the child's env.
type classicBatchChildConfig struct {
	logDir   string
	from     uint64
	total    uint64
	paceMS   int
	settings string
	block    bool
}

func (c classicBatchChildConfig) env() []string {
	return []string{
		envChild + "=1",
		envClassicBatchMode + "=" + envValueTrue,
		envClassicBatchLogDir + "=" + c.logDir,
		envClassicBatchFrom + "=" + strconv.FormatUint(c.from, 10),
		envClassicBatchTotal + "=" + strconv.FormatUint(c.total, 10),
		envClassicBatchPaceMS + "=" + strconv.Itoa(c.paceMS),
		envClassicBatchSettings + "=" + c.settings,
		envClassicBatchBlock + "=" + strconv.FormatBool(c.block),
	}
}

// fsyncConnector is the fake connector behind the SDK: Write makes every
// record durable (deliveryLog.Record fsyncs) and only then returns the count,
// which is what the SDK acks.
type fsyncConnector struct {
	sdk.UnimplementedDestination

	log   *deliveryLog
	block bool
	once  sync.Once
	// cfg must be the same pointer on every Config() call: the SDK middleware
	// captures it at wrap time and the adapter parses settings into it later.
	cfg *struct{ sdk.DestinationWithBatch }
}

func (c *fsyncConnector) Config() sdk.DestinationConfig { return c.cfg }

func (*fsyncConnector) Open(context.Context) error     { return nil }
func (*fsyncConnector) Teardown(context.Context) error { return nil }

func (c *fsyncConnector) Write(ctx context.Context, recs []opencdc.Record) (int, error) {
	fmt.Printf("%s%d\n", markerClassicWrite, len(recs))
	if c.block {
		blocked := false
		c.once.Do(func() { blocked = true })
		if blocked {
			fmt.Printf("%s%d\n", markerClassicBlock, len(recs))
			<-ctx.Done() // never made durable; the parent SIGKILLs us here
			select {}
		}
	}
	for i, r := range recs {
		pos, err := strconv.ParseUint(string(r.Position), 10, 64)
		if err != nil {
			return i, err
		}
		if err := c.log.Record("", pos); err != nil {
			return i, err
		}
	}
	return len(recs), nil
}

// batchDispenserFetcher hands out the one dispenser.
type batchDispenserFetcher struct{ d connectorPlugin.Dispenser }

func (f batchDispenserFetcher) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	return f.d, nil
}

func parseSettings(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// runChildClassicBatch never returns; it always os.Exit's.
func runChildClassicBatch() {
	ctx := context.Background()
	from, _ := strconv.ParseUint(os.Getenv(envClassicBatchFrom), 10, 64)
	total, _ := strconv.ParseUint(os.Getenv(envClassicBatchTotal), 10, 64)
	paceMS, _ := strconv.Atoi(os.Getenv(envClassicBatchPaceMS))
	block := os.Getenv(envClassicBatchBlock) == envValueTrue

	dlog, err := openDeliveryLog(os.Getenv(envClassicBatchLogDir))
	if err != nil {
		fmt.Printf("%s %v\n", markerFatal, err)
		os.Exit(exitCorruptState)
	}

	logger := log.New(zerolog.Nop())
	params := config.Parameters{
		"sdk.batch.size":  {Type: config.ParameterTypeInt},
		"sdk.batch.delay": {Type: config.ParameterTypeDuration},
	}
	fake := &fsyncConnector{log: dlog, block: block, cfg: &struct{ sdk.DestinationWithBatch }{}}
	dispenser := builtin.NewDispenser(
		plugin.FullName("builtin:postgres"),
		logger,
		nil, nil,
		func() pconnector.DestinationPlugin {
			return sdk.NewDestinationPlugin(sdk.DestinationWithMiddleware(fake), pconnector.PluginConfig{}, params)
		},
	)

	persister := connector.NewPersister(logger, &inmemory.DB{}, connector.DefaultPersisterDelayThreshold, 1)
	instance := &connector.Instance{
		ID:            "classic-batch-dest",
		Type:          connector.TypeDestination,
		Config:        connector.Config{Name: "classic-batch", Settings: parseSettings(os.Getenv(envClassicBatchSettings))},
		PipelineID:    "classic-batch-pipeline",
		Plugin:        "postgres",
		ProvisionedBy: connector.ProvisionTypeConfig,
	}
	instance.LastActiveConfig = instance.Config // no lifecycle "created" event
	instance.Init(logger, persister)

	conn, err := instance.Connector(ctx, batchDispenserFetcher{dispenser})
	if err != nil {
		fmt.Printf("%s %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	dest := conn.(*connector.Destination)
	dest.SetClassicDefaultBatching() // what pkg/lifecycle.buildDestinationNodes does
	if err := dest.Open(ctx); err != nil {
		fmt.Printf("%s %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	want := int(total - from + 1)
	acked := make(chan struct{})
	go func() {
		defer close(acked)
		n := 0
		for n < want {
			acks, err := dest.Ack(ctx)
			if err != nil {
				return
			}
			for _, a := range acks {
				if a.Error != nil {
					fmt.Printf("%s %v\n", markerFatal, a.Error)
					os.Exit(exitCorruptState)
				}
				fmt.Printf("%s%s\n", markerClassicAcked, a.Position)
				n++
			}
		}
	}()

	for pos := from; pos <= total; pos++ {
		rec := opencdc.Record{Position: opencdc.Position(strconv.FormatUint(pos, 10)), Operation: opencdc.OperationCreate}
		if err := dest.Write(ctx, []opencdc.Record{rec}); err != nil {
			fmt.Printf("%s %v\n", markerFatal, err)
			os.Exit(exitCorruptState)
		}
		if paceMS > 0 {
			time.Sleep(time.Duration(paceMS) * time.Millisecond)
		}
	}

	// Graceful end: Stop flushes the pending batch (invariant 7), then every
	// record must be acked.
	if err := dest.Stop(ctx, opencdc.Position(strconv.FormatUint(total, 10))); err != nil {
		fmt.Printf("%s %v\n", markerFatal, err)
		os.Exit(exitCorruptState)
	}
	select {
	case <-acked:
	case <-time.After(30 * time.Second):
		fmt.Printf("%s timed out waiting for acks\n", markerFatal)
		os.Exit(exitDrainTooSlow)
	}
	if err := dest.Teardown(ctx); err != nil {
		fmt.Printf("%s %v\n", markerFatal, err)
		os.Exit(exitCorruptState)
	}
	fmt.Println(markerClassicDone)
	os.Exit(exitOK)
}
