// Copyright © 2024 Meroxa, Inc.
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

//go:generate paramgen -output=error_paramgen.go errorConfig

package impl

import (
	"context"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type errorConfig struct {
	// Error message to be returned. This can be a Go [template](https://pkg.go.dev/text/template)
	// executed on each [`Record`](https://pkg.go.dev/github.com/conduitio/conduit-commons/opencdc#Record)
	// being processed.
	Message string `json:"message" default:"error processor triggered"`
}

type errorProcessor struct {
	sdk.UnimplementedProcessor

	config           errorConfig
	errorMessageTmpl *template.Template
}

func NewErrorProcessor(log.CtxLogger) sdk.Processor {
	return &errorProcessor{}
}

func (p *errorProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "error",
		Summary: "Returns an error for all records that get passed to the processor.",
		Description: `Any time a record is passed to this processor it returns an error,
which results in the record being sent to the DLQ if it's configured, or the pipeline stopping.

**Important:** Make sure to add a [condition](https://conduit.io/docs/processors/conditions)
to this processor, otherwise all records will trigger an error.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: p.config.Parameters(),
	}, nil
}

func (p *errorProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, p.config.Parameters())
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	if strings.Contains(p.config.Message, "{{") {
		// create URL template
		p.errorMessageTmpl, err = template.New("").Funcs(sprig.FuncMap()).Parse(p.config.Message)
		if err != nil {
			return cerrors.Errorf("error while parsing the error message template: %w", err)
		}
	}

	return nil
}

func (p *errorProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i := range records {
		out[i] = sdk.ErrorRecord{
			Error: cerrors.New(p.errorMessage(records[i])),
		}
	}
	return out
}

func (p *errorProcessor) errorMessage(record opencdc.Record) string {
	if p.errorMessageTmpl == nil {
		return p.config.Message
	}

	var buf strings.Builder
	if err := p.errorMessageTmpl.Execute(&buf, record); err != nil {
		return err.Error()
	}
	return buf.String()
}

func (*errorProcessor) MiddlewareOptions() []sdk.ProcessorMiddlewareOption {
	// disable schema middleware by default
	return []sdk.ProcessorMiddlewareOption{
		sdk.ProcessorWithSchemaEncodeConfig{
			PayloadEnabled: lang.Ptr(false),
			KeyEnabled:     lang.Ptr(false),
		},
		sdk.ProcessorWithSchemaDecodeConfig{
			PayloadEnabled: lang.Ptr(false),
			KeyEnabled:     lang.Ptr(false),
		},
	}
}
