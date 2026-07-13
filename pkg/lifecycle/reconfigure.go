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
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
)

// ErrProcessorNotLiveReconfigurable signals that a processor cannot be swapped in
// place on the running pipeline, so the caller must fall back to a restart-class
// apply. It is returned when the processor is not running as a plain,
// single-worker stream.ProcessorNode — e.g. it is parallelized (Workers > 1, and
// so runs as a stream.ParallelNode), or no matching node is present. Restarting
// is always a safe superset: the restart path re-imports the config and rebuilds
// every node, so it applies the change regardless of the running node's shape.
var ErrProcessorNotLiveReconfigurable = cerrors.New("processor is not live-reconfigurable, a restart is required")

// ReconfigureProcessor swaps a processor's configuration in a running pipeline in
// place — without stopping or restarting the pipeline — by rebuilding the
// runnable processor from its (already-updated) stored instance and swapping it
// into the live stream.ProcessorNode via ProcessorNode.Reconfigure. See the
// design doc "Live in-place hot-reload".
//
// Ordering contract: the caller MUST persist the processor's new config to its
// instance before calling this (ReconfigureProcessor reads the current instance
// from the processor service). The swap happens at a record boundary; open of
// the new processor happens before teardown of the old, so if the new config
// fails to open, the old processor keeps running and the error is returned — the
// pipeline never drops (invariant 3).
//
// Returns ErrProcessorNotLiveReconfigurable if the processor is not a plain
// single-worker node in the running pipeline; the caller should then fall back to
// a restart. Returns an error (old processor kept) if building or opening the new
// processor fails.
func (s *Service) ReconfigureProcessor(ctx context.Context, pipelineID, processorID string) error {
	rp, ok := s.runningPipelines.Get(pipelineID)
	if !ok {
		return cerrors.Errorf("pipeline %q is not running, cannot reconfigure processor %q in place", pipelineID, processorID)
	}

	// A single-worker processor runs as a *stream.ProcessorNode named for the
	// processor ID. A parallel processor (Workers > 1) runs as a
	// *stream.ParallelNode instead and is not matched here, so it falls through
	// to the restart fallback below — as does a processor with no live node.
	var node *stream.ProcessorNode
	for _, n := range rp.n {
		pn, isProcNode := n.(*stream.ProcessorNode)
		if isProcNode && pn.ID() == processorID {
			node = pn
			break
		}
	}
	if node == nil {
		return cerrors.Errorf("%w: processor %q in pipeline %q", ErrProcessorNotLiveReconfigurable, processorID, pipelineID)
	}

	instance, err := s.processors.Get(ctx, processorID)
	if err != nil {
		return cerrors.Errorf("could not fetch processor %q for live reconfigure: %w", processorID, err)
	}
	runnableProc, err := s.processors.MakeRunnableProcessorForReconfigure(ctx, instance)
	if err != nil {
		return cerrors.Errorf("could not build runnable processor %q for live reconfigure: %w", processorID, err)
	}

	return node.Reconfigure(ctx, runnableProc)
}
