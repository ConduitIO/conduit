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

package debezium

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// Processor is the debezium.encode built-in processor. It turns one
// Debezium-mode record (typed values, debezium.* metadata, an in-record
// descriptor) into the converter bytes and the message plan Debezium would
// have produced for that WAL change, and writes the plan into Payload.After
// with kafka.messages.version=1 for the Kafka destination.
type Processor struct {
	sdk.UnimplementedProcessor

	logger log.CtxLogger
	cfg    encodeConfig
	// now returns the encode wall clock (D1: the envelope timestamps are
	// volatile). Tests replace it to make output deterministic.
	now func() time.Time
}

// NewProcessor returns a debezium.encode processor.
func NewProcessor(logger log.CtxLogger) *Processor {
	return &Processor{logger: logger, now: time.Now}
}

// Specification returns the processor specification.
func (p *Processor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "debezium.encode",
		Summary: "Encodes a Debezium-mode Postgres CDC record into the Debezium envelope and message plan.",
		Description: `Turns one record from the Postgres source's Debezium mode (debezium.enabled=true)
into the messages Debezium would have produced: the before/after envelope, the source block, the
operation and tombstone semantics. A delete expands into a delete event plus a tombstone, and a
primary-key change expands into delete, tombstone and create.

The result is a message plan in Payload.After (kafka.messages.version=1) that the Kafka destination
writes as one acknowledged unit. Run this processor last, directly before the Kafka destination.`,
		Version:    "v0.1.0",
		Author:     "ConduitIO",
		Parameters: encodeConfig{}.Parameters(),
	}, nil
}

// Configure parses and validates the processor settings.
func (p *Processor) Configure(ctx context.Context, c config.Config) error {
	cfg, err := parseConfig(ctx, c)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}
	if cfg.KeyConverter == converterAvro || cfg.ValueConverter == converterAvro {
		return newCoded(CodeAvroUnsupported,
			"the Avro converter is not implemented yet; use key.converter=json and value.converter=json",
			"The Confluent AvroConverter (AvroData port, subject naming, Confluent wire format) is the design's default and lands in the next revision.")
	}
	if cfg.Compat != Compat26 && cfg.Compat != Compat35 {
		return newCoded(CodeConfigInvalid,
			"debezium.compat must be 2.6 or 3.5",
			"Set debezium.compat to the Debezium version boundary the source emulates.")
	}
	p.cfg = cfg
	return nil
}

// Process encodes each record into its message plan.
func (p *Processor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc, err := p.processRecord(rec)
		if err != nil {
			out = append(out, sdk.ErrorRecord{Error: err})
			continue
		}
		out = append(out, proc)
	}
	return out
}

func (p *Processor) processRecord(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	d, err := parseDescriptor(rec.Metadata[metaDescriptor])
	if err != nil {
		return nil, err
	}

	now := p.now()
	msgs, err := expandPlan(rec, d, p.cfg, now)
	if err != nil {
		return nil, err
	}

	frameBytes, err := encodeFrame(msgs)
	if err != nil {
		return nil, cerrors.Errorf("failed encoding message plan: %w", err)
	}

	rec.Payload.Before = nil
	rec.Payload.After = opencdc.RawData(frameBytes)
	if rec.Metadata == nil {
		rec.Metadata = opencdc.Metadata{}
	}
	rec.Metadata[metaPlanVersion] = planVersion
	return sdk.SingleRecord(rec), nil
}

// Teardown is a no-op.
func (p *Processor) Teardown(context.Context) error {
	return nil
}
