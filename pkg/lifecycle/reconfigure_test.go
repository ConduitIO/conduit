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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

func newReconfigureTestService() *Service {
	return NewService(
		log.Nop(),
		testErrRecoveryCfg(),
		testConnectorService{},
		testProcessorService{},
		testConnectorPluginService{},
		testPipelineService{},
	)
}

// TestService_ReconfigureProcessor_NotRunning: reconfiguring a processor in a
// pipeline that is not currently running is an error (nothing live to swap).
func TestService_ReconfigureProcessor_NotRunning(t *testing.T) {
	is := is.New(t)
	s := newReconfigureTestService()

	err := s.ReconfigureProcessor(context.Background(), "does-not-exist", "proc1")
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "not running"))
}

// TestService_ReconfigureProcessor_NoPlainProcessorNode_FallsBackToRestart: when
// the running pipeline has no plain single-worker ProcessorNode for the target
// (it is absent, or parallelized as a ParallelNode), ReconfigureProcessor returns
// ErrProcessorNotLiveReconfigurable so the caller falls back to a restart.
func TestService_ReconfigureProcessor_NoPlainProcessorNode_FallsBackToRestart(t *testing.T) {
	is := is.New(t)
	s := newReconfigureTestService()

	// A running pipeline whose nodes include no *stream.ProcessorNode named
	// "proc1" — stands in for both the "parallelized" and "absent" cases, which
	// both must degrade to a restart.
	s.runningPipelines.Set("pl1", &runnablePipeline{
		pipeline: &pipeline.Instance{ID: "pl1"},
		n:        []stream.Node{&stream.FaninNode{}, &stream.FanoutNode{}},
	})

	err := s.ReconfigureProcessor(context.Background(), "pl1", "proc1")
	is.True(cerrors.Is(err, ErrProcessorNotLiveReconfigurable))
}
