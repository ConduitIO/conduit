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

//go:generate go test -count=1 -tags export_processors .

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/diff"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// -- HELPERS ------------------------------------------------------------------

var processors = map[string]*procInfo{}

type procInfo struct {
	Specification sdk.Specification `json:"specification"`
	Examples      []example         `json:"examples"`
}

type example struct {
	// Order is an optional field that is used to order examples in the
	// documentation. If omitted, the example will be ordered by description.
	Order       int                 `json:"-"`
	Description string              `json:"description"`
	Config      map[string]string   `json:"config"`
	Have        opencdc.Record      `json:"have"`
	Want        sdk.ProcessedRecord `json:"want"`
}

// RunExample runs the given example with the given processor and logs the
// result. It is intended to be used in example functions. Additionally, it
// stores the processor specification and example in a global map so it can be
// used to generate documentation.
func RunExample(p sdk.Processor, e example) {
	spec, err := p.Specification()
	if err != nil {
		log.Fatalf("failed to fetch specification: %v", err)
	}

	pi, ok := processors[spec.Name]
	if !ok {
		pi = &procInfo{Specification: spec}
		processors[spec.Name] = pi
	}

	ctx := context.Background()
	err = p.Configure(ctx, e.Config)
	if err != nil {
		log.Fatalf("failed to configure processor: %v", err)
	}

	err = p.Open(ctx)
	if err != nil {
		log.Fatalf("failed to open processor: %v", err)
	}

	got := p.Process(ctx, []opencdc.Record{e.Have.Clone()})
	if len(got) != 1 {
		log.Fatalf("expected 1 record to be returned, got %d", len(got))
	}

	if d := cmp.Diff(e.Want, got[0], cmpopts.IgnoreUnexported(sdk.SingleRecord{})); d != "" {
		log.Fatalf("processed record did not match expectation:\n%v", d)
	}

	switch rec := got[0].(type) {
	case sdk.SingleRecord:
		// produce JSON diff
		havePrettyJSON, err := json.MarshalIndent(e.Have, "", "  ")
		if err != nil {
			log.Fatalf("failed to marshal test record to JSON: %v", err)
		}

		gotPrettyJSON, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			log.Fatalf("failed to marshal processed record to JSON: %v", err)
		}

		edits := diff.Strings(string(havePrettyJSON), string(gotPrettyJSON))
		unified, err := diff.ToUnified("before", "after", string(havePrettyJSON)+"\n", edits, 100)
		if err != nil {
			log.Fatalf("failed to produce unified diff: %v", err)
		}

		fmt.Printf("processor transformed record:\n%s\n", unified)
	case sdk.FilterRecord:
		fmt.Println("processor filtered record out")
	case sdk.ErrorRecord:
		fmt.Printf("processor returned error: %s\n", rec.Error)
	}

	// append example to processor
	pi.Examples = append(pi.Examples, e)
}
