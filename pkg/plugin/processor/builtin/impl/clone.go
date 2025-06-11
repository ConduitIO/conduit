// Copyright © 2025 Meroxa, Inc.
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

//go:generate paramgen -output=clone_paramgen.go cloneConfig

package impl

import (
	"context"
	"strconv"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type CloneProcessor struct {
	sdk.UnimplementedProcessor

	config cloneConfig
}

func NewCloneProcessor(log.CtxLogger) *CloneProcessor { return &CloneProcessor{} }

type cloneConfig struct {
	// The number of records after cloning (e.g. if count is 3, the processor
	// will output 3 records for every input record).
	Count int `json:"count" validate:"required,gt=1"`
}

func (p *CloneProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "clone",
		Summary: "Clone records.",
		Description: `Clone all records N times. All cloned records will have
the same data as the original record, except for the metadata field
` + "`clone.index`" + `, which will be set to the index of the clone (0-based).

**Important:** Add a [condition](https://conduit.io/docs/using/processors/conditions)
to this processor if you only want to clone some records.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: cloneConfig{}.Parameters(),
	}, nil
}

func (p *CloneProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, cloneConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	return nil
}

func (p *CloneProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, rec := range records {
		mr := make(sdk.MultiRecord, p.config.Count)
		for j := range p.config.Count {
			newRec := rec.Clone()
			// Set the metadata to indicate the clone index
			if newRec.Metadata == nil {
				newRec.Metadata = make(map[string]string)
			}
			newRec.Metadata["clone.index"] = strconv.Itoa(j)

			mr[j] = newRec
		}
		out[i] = mr
	}
	return out
}
