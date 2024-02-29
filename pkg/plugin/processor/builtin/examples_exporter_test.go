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

//go:build export_processors

package builtin

import (
	"context"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/goccy/go-json"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code > 0 {
		os.Exit(code)
	}

	// tests passed, export the processors
	const outputFile = "processors.json"

	f, err := os.OpenFile(outputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("failed to open %s: %v", outputFile, err)
	}
	defer f.Close()

	exportProcessors(f)
}

func exportProcessors(output io.Writer) {
	sorted := sortProcessors(processors)

	ctx := opencdc.WithJSONMarshalOptions(context.Background(), &opencdc.JSONMarshalOptions{RawDataAsString: true})
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")

	err := enc.EncodeContext(ctx, sorted)
	if err != nil {
		log.Fatalf("failed to write processors to output: %v", err)
	}
}

func sortProcessors(processors map[string]*procInfo) []*procInfo {
	names := make([]string, 0, len(processors))
	for k, _ := range processors {
		names = append(names, k)
	}
	sort.Strings(names)

	sorted := make([]*procInfo, len(names))
	for i, name := range names {
		// also sort examples for each processor
		proc := processors[name]
		proc.Examples = sortExamples(proc.Examples)
		sorted[i] = proc
	}

	return sorted
}

func sortExamples(examples []example) []example {
	sort.Slice(examples, func(i, j int) bool {
		if examples[i].Order != examples[j].Order {
			return examples[i].Order < examples[j].Order
		}
		return strings.Compare(examples[i].Description, examples[j].Description) < 0
	})
	return examples
}
