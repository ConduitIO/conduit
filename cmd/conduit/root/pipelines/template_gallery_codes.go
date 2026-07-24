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

package pipelines

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeUnknownTemplate is raised by `pipelines init --template <name>` when
// name doesn't match any entry in the embedded template gallery (see
// template_gallery.go). codes.InvalidArgument classifies to
// exitcode.Validation (2): the fix is on the caller's side (a typo, or a
// template that doesn't exist yet), not the environment.
var CodeUnknownTemplate = conduiterr.Register("pipelines.init_unknown_template", codes.InvalidArgument)

// CodeTemplateFlagsExclusive is raised when --template (including the
// literal value "list") is combined with --source/--destination. A named
// template already fully determines the source+destination+settings triple;
// mixing it with --source/--destination is ambiguous by construction, so
// this is rejected rather than silently picking a winner (templates
// workstream doc §7).
var CodeTemplateFlagsExclusive = conduiterr.Register("pipelines.init_template_flags_exclusive", codes.InvalidArgument)

// CodeTemplateVersionMismatch is raised when an embedded template's pinned
// connector settings no longer match the parameter shape of the built-in
// connector actually compiled into this binary. This is a packaging bug in
// the vendored template (the fixture drifted from the connector version
// this release ships), not a user config error — refusing up front here is
// strictly better than scaffolding a pipeline that parses fine but fails far
// away, and confusingly, at `conduit run` (templates workstream doc §7's
// "version-pinned mismatch" row). codes.FailedPrecondition classifies to
// exitcode.Validation (2).
var CodeTemplateVersionMismatch = conduiterr.Register("pipelines.init_template_version_mismatch", codes.FailedPrecondition)
