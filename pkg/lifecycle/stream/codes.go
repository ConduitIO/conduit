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
// split, clone) on the default (classic) pipeline engine, which is
// one-record-in-one-record-out and cannot express fan-out. Fan-out is supported
// only by pipeline architecture v2 (pkg/lifecycle-poc/funnel), enabled via
// --preview.pipeline-arch-v2. FailedPrecondition: the pipeline as configured
// cannot run on this engine.
var CodeFanOutRequiresArchV2 = conduiterr.Register("pipeline.fanout_requires_arch_v2", codes.FailedPrecondition)
