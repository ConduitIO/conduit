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

package exampleutil

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/diff"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
)

// -- HELPERS ------------------------------------------------------------------

var processors = map[string]*procInfo{}

type procInfo struct {
	Specification sdk.Specification `json:"specification"`
	Examples      []Example         `json:"examples"`
}

type Example struct {
	// Order is an optional field that is used to order examples in the
	// documentation. If omitted, the example will be ordered by description.
	Order       int                 `json:"-"`
	Summary     string              `json:"summary"`
	Description string              `json:"description"`
	Config      config.Config       `json:"config"`
	Have        opencdc.Record      `json:"have"`
	Want        sdk.ProcessedRecord `json:"want"`
}

// RunExample runs the given example with the given processor and logs the
// result. It is intended to be used in example functions. Additionally, it
// stores the processor specification and example in a global map so it can be
// used to generate documentation.
func RunExample(p sdk.Processor, e Example) {
	// apply default middleware
	p = sdk.ProcessorWithMiddleware(p, sdk.DefaultProcessorMiddleware(p.MiddlewareOptions()...)...)

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

	if d := cmp.Diff(e.Want, got[0], internal.CmpProcessedRecordOpts...); d != "" {
		log.Fatalf("processed record did not match expectation:\n%v", d)
	}

	msg, err := formatProcessedRecord(e.Have, got[0])
	if err != nil {
		log.Fatalf("failed to format processed record: %v", err)
	}
	fmt.Println(msg)

	// append example to processor
	pi.Examples = append(pi.Examples, e)
}

func formatProcessedRecord(have opencdc.Record, got sdk.ProcessedRecord) (string, error) {
	switch got := got.(type) {
	case sdk.FilterRecord:
		return "processor filtered record out", nil
	case sdk.ErrorRecord:
		return fmt.Sprintf("processor returned error: %s\n", got.Error), nil
	case sdk.SingleRecord:
		// Serialize records to pretty JSON for comparison.
		havePrettyJSON, err := recordToPrettyJSON(have, "")
		if err != nil {
			return "", fmt.Errorf("failed to marshal test record to pretty JSON: %v", err)
		}
		gotPrettyJSON, err := recordToPrettyJSON(opencdc.Record(got), "")
		if err != nil {
			return "", fmt.Errorf("failed to marshal processed record to pretty JSON: %v", err)
		}

		unified, err := generateUnifiedDiff(havePrettyJSON, gotPrettyJSON)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("processor transformed record:\n%s\n", unified), nil
	case sdk.MultiRecord:
		// Serialize records to pretty JSON for comparison.
		havePrettyJSON, err := recordToPrettyJSON(have, "")
		if err != nil {
			return "", fmt.Errorf("failed to marshal test record to pretty JSON: %v", err)
		}

		gotPrettyJSON, err := multiRecordToPrettyJSON(got)
		if err != nil {
			return "", fmt.Errorf("failed to marshal processed record to pretty JSON: %v", err)
		}

		unified, err := generateUnifiedDiff(havePrettyJSON, gotPrettyJSON)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("processor transformed record:\n%s\n", unified), nil
	default:
		return "", fmt.Errorf("unknown processed record type: %T", got)
	}
}

func recordToPrettyJSON(r opencdc.Record, prefix string) ([]byte, error) {
	serializer := opencdc.JSONSerializer{RawDataAsString: true}

	// Serialize records to pretty JSON for comparison.
	haveJSON, err := serializer.Serialize(r)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal test record to JSON: %w", err)
	}
	var buf bytes.Buffer
	err = json.Indent(&buf, haveJSON, prefix, "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to indent test record JSON: %w", err)
	}
	return buf.Bytes(), nil
}

func multiRecordToPrettyJSON(r sdk.MultiRecord) ([]byte, error) {
	// Serialize records to pretty JSON for comparison.
	var buf bytes.Buffer
	buf.WriteString("[\n")

	// Indent each record with 2 spaces
	prefix := "  "
	for i, subRec := range r {
		prettyJSON, err := recordToPrettyJSON(subRec, prefix)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal processed record to pretty JSON: %w", err)
		}
		buf.WriteString(prefix)
		buf.Write(prettyJSON)
		if i < len(r)-1 {
			buf.WriteString(",\n")
		}
	}
	buf.WriteString("\n]")
	return buf.Bytes(), nil
}

func generateUnifiedDiff(before, after []byte) (string, error) {
	edits := diff.Bytes(before, after)
	unified, err := diff.ToUnified("before", "after", string(before)+"\n", edits, 100)
	if err != nil {
		return "", fmt.Errorf("failed to produce unified diff: %w", err)
	}
	return unified, nil
}
