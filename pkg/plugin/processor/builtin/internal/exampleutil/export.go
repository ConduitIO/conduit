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
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// ExportProcessors exports the processors to the specs directory.
func ExportProcessors() {
	_, filename, _, _ := runtime.Caller(0) //nolint:dogsled // this is the idiomatic way to get the current file's path
	dir := filepath.Join(filepath.Dir(filename), "specs")

	for _, processor := range sortProcessors(processors) {
		path := filepath.Join(dir, processor.Specification.Name+".json")
		err := exportProcessor(path, processor)
		if err != nil {
			log.Fatalf("error: %v", err)
		}
	}
}

func exportProcessor(path string, processor *procInfo) error {
	output, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return cerrors.Errorf("failed to open %s: %w", path, err)
	}
	defer output.Close()

	ctx := opencdc.WithJSONMarshalOptions(context.Background(), &opencdc.JSONMarshalOptions{RawDataAsString: true})
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")

	err = enc.EncodeContext(ctx, processor)
	if err != nil {
		return cerrors.Errorf("failed to write processors to output: %w", err)
	}
	return nil
}

func sortProcessors(processors map[string]*procInfo) []*procInfo {
	names := make([]string, 0, len(processors))
	for name := range processors {
		names = append(names, name)
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

func sortExamples(examples []Example) []Example {
	sort.Slice(examples, func(i, j int) bool {
		if examples[i].Order != examples[j].Order {
			return examples[i].Order < examples[j].Order
		}
		return strings.Compare(examples[i].Description, examples[j].Description) < 0
	})
	return examples
}
