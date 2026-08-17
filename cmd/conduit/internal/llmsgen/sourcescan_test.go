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

package main

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/matryer/is"
)

// descriptionsFor scans src as a Go file and returns reason -> description for
// every registered code it declares.
func descriptionsFor(t *testing.T, src string) map[string]string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "codes.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write temp source: %v", err)
	}

	got, err := scanFileForRegisterCalls(token.NewFileSet(), path, "codes.go")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	out := make(map[string]string, len(got))
	for _, c := range got {
		out[c.Reason] = c.Description
	}
	return out
}

// A doc comment on a standalone `var X = Register(...)` attaches to the
// declaration, not to the single ValueSpec inside it. Reading only the spec
// left every such code with an empty description, which the renderer then
// filled with a humanized reason ("pipelines: init unknown template") — a
// placeholder shipped in the agent-facing error reference.
func TestScanFileForRegisterCalls_StandaloneVarUsesItsDocComment(t *testing.T) {
	is := is.New(t)

	got := descriptionsFor(t, `package p

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeAlone explains itself in a real sentence.
var CodeAlone = conduiterr.Register("domain.alone", codes.Internal)
`)

	is.Equal(got["domain.alone"], "CodeAlone explains itself in a real sentence.")
}

// The fallback must NOT reach into a `var ( ... )` block's own comment: that
// paragraph describes the group. Inheriting it stamps the same prose onto
// every uncommented code in the block, which reads as if it had been written
// about that specific code — worse than an empty description.
func TestScanFileForRegisterCalls_BlockCommentNotInheritedBySpecs(t *testing.T) {
	is := is.New(t)

	got := descriptionsFor(t, `package p

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Codes owned by this package. This sentence describes the GROUP.
var (
	// CodeDocumented has its own doc comment.
	CodeDocumented = conduiterr.Register("domain.documented", codes.Internal)

	CodeBare = conduiterr.Register("domain.bare", codes.Internal)
)
`)

	is.Equal(got["domain.documented"], "CodeDocumented has its own doc comment.")
	is.Equal(got["domain.bare"], "")
}

// go/ast associates every preceding comment group with the following node, so
// a detached note above a declaration (rationale, TODO, a rebase reminder)
// would otherwise be concatenated into the shipped description. Only the
// nearest group — the actual doc comment — belongs there.
func TestScanFileForRegisterCalls_DetachedNoteNotConcatenated(t *testing.T) {
	is := is.New(t)

	got := descriptionsFor(t, `package p

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// This file is additive only by design: a parallel change is touching this
// package, so expect to rebase. Repo-process prose, not user documentation.

// CodeNoted is raised when the thing the user cares about happens.
var CodeNoted = conduiterr.Register("domain.noted", codes.Unavailable)
`)

	is.Equal(got["domain.noted"], "CodeNoted is raised when the thing the user cares about happens.")
}

// A trailing comment on the declaration's own line is not a doc comment.
func TestScanFileForRegisterCalls_TrailingCommentIgnored(t *testing.T) {
	is := is.New(t)

	got := descriptionsFor(t, `package p

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

var CodeTrailing = conduiterr.Register("domain.trailing", codes.Internal) // not documentation
`)

	is.Equal(got["domain.trailing"], "")
}
