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

package stream

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeFanOutRequiresArchV2 is raised when a processor returns a fan-out result
// (sdk.MultiRecord — one input record producing N output records, e.g. ai.chunk,
// split, clone) on the classic default pipeline engine, which is
// one-record-in-one-record-out and cannot run it. Record fan-out needs the
// preview engine, pipeline architecture v2 (pkg/lifecycle-poc/funnel), enabled
// with --preview.pipeline-arch-v2 or preview.pipeline-arch-v2: true; the
// postgres-pgvector-rag template is one such pipeline. Graduation of v2 to the
// default engine is evaluated in v0.21 against a written gate. FailedPrecondition:
// the pipeline as configured cannot run on this engine.
var CodeFanOutRequiresArchV2 = conduiterr.Register("pipeline.fanout_requires_arch_v2", codes.FailedPrecondition)
