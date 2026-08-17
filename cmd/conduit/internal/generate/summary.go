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

package generate

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/log"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// CandidateSummary is what a caller can say about a generated candidate
// without re-parsing it: the pipeline's own id (which is where a default
// output filename comes from) and how much is in it.
type CandidateSummary struct {
	PipelineID string
	Connectors int
	Processors int
}

// Summarize reads a candidate with the same parser validate uses.
//
// It is only ever called on a candidate that already passed the validate
// gate, so a parse failure here is not a user-facing error — it returns a
// zero summary and lets the caller fall back (to a generic filename, or to
// omitting counts). Re-reporting a parse problem at this point would
// contradict the report that just said the config is fine.
func Summarize(ctx context.Context, candidate string) CandidateSummary {
	pipelines, err := yamlparser.NewParser(log.Nop()).Parse(ctx, strings.NewReader(candidate))
	if err != nil || len(pipelines) == 0 {
		return CandidateSummary{}
	}

	p := pipelines[0]
	s := CandidateSummary{
		PipelineID: p.ID,
		Connectors: len(p.Connectors),
		Processors: len(p.Processors),
	}
	for _, c := range p.Connectors {
		s.Processors += len(c.Processors)
	}
	return s
}
