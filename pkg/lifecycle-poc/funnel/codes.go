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

package funnel

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeDuplicateSourcePosition is returned when a single batch read from a
// source contains two or more records carrying identical position bytes, and
// that batch is fanned out to multiple destinations.
//
// A position is a record's identity: multiAckNacker keys its per-position
// unanimity tally on it, and connector.Source.Ack advances the persisted
// position from it. Two records sharing one position make those two things
// ambiguous — the tally cannot tell the records apart, so one of them could
// never reach unanimity, and because positions are released to the source
// strictly in order (invariant 4), a single unresolvable slot silently stops
// the WHOLE batch from ever being acked. The pipeline would keep running,
// reading and writing records, while its source position never advanced and
// no error was ever surfaced.
//
// Failing loudly here is deliberate. At-least-once is not violated by the
// stall (nothing is acked, so nothing is lost — the records replay on
// restart), but a silent, unbounded liveness failure is far worse for an
// operator than a clear error naming the offending connector behaviour.
// Emitting duplicate positions within one batch is a connector contract
// violation, so this is a user-actionable error, not an internal assertion.
var CodeDuplicateSourcePosition = conduiterr.Register("pipeline.duplicate_source_position", codes.FailedPrecondition)
