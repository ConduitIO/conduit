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

package template

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// tmplSuffix is appended to every "go.mod" and "*.go" path in the vendored
// snapshot (go.mod -> go.mod.tmpl, connector.go -> connector.go.tmpl; see
// connector/ and processor/ on disk). This is not decorative: a literal
// go.mod file makes the go tool treat its directory as a separate module,
// and //go:embed refuses to embed a directory that belongs to a different
// module ("pattern all:connector: cannot embed directory connector: in
// different module") — so without the rename, embedding would fail
// outright. Leaving the snapshot's *.go files with a real .go suffix has a
// second, worse failure mode even where a go.mod boundary is present for
// the connector/processor module itself: connector/tools and
// processor/tools each have their own go.mod one level deeper, and without
// renaming there would be nothing stopping this repository's own `go build
// ./...`, `go generate ./...`, or golangci-lint from attempting to treat
// stray *.go files elsewhere in the tree as ordinary source belonging to
// this module (they are not — their imports don't resolve against this
// module's go.mod, and their package names collide with nothing on
// purpose, but the go tool has no way to know that without a module
// boundary or a non-Go suffix to exclude them by). Extract reverses the
// rename on write-out (see its doc), so the tree scaffold.Generate hands to
// package steps and package steps' `go build ./...` verification is a
// completely ordinary Go module with real go.mod/*.go filenames.
const tmplSuffix = ".tmpl"

// Kind selects which template snapshot to use.
type Kind string

const (
	// KindConnector is github.com/ConduitIO/conduit-connector-template.
	KindConnector Kind = "connector"
	// KindProcessor is github.com/ConduitIO/conduit-processor-template.
	KindProcessor Kind = "processor"
)

// connectorFS and processorFS are the vendored template snapshots. The
// "all:" prefix is required, not decorative: both templates have dotfiles
// and dot-directories that matter (.golangci.yml, .goreleaser.yml,
// .gitignore, .github/workflows/*) and go:embed silently drops any file or
// directory whose name starts with "." or "_" unless the pattern is
// prefixed with "all:" (see the embed package doc). Without it, Extract
// would produce a scaffold missing its own CI, lint config, and
// dependabot/release automation with no error to signal the gap.
//
//go:embed all:connector
var connectorFS embed.FS

//go:embed all:processor
var processorFS embed.FS

const (
	// ConnectorRef is the git commit SHA of ConduitIO/conduit-connector-template
	// that connector/ was synced from.
	ConnectorRef = "99a312dd42f56027a0c7171e406bf3642194eca5"
	// ProcessorRef is the git commit SHA of ConduitIO/conduit-processor-template
	// that processor/ was synced from.
	ProcessorRef = "ace2924f861c1c309fe144a47336d89dd6864611"

	// ConnectorSDKVersion is the github.com/conduitio/conduit-connector-sdk
	// version pinned in connector/go.mod at ConnectorRef.
	ConnectorSDKVersion = "v0.14.1"
	// ProcessorSDKVersion is the github.com/conduitio/conduit-processor-sdk
	// version pinned in processor/go.mod at ProcessorRef.
	ProcessorSDKVersion = "v0.5.0"
)

// Ref returns the pinned upstream git SHA for kind.
func (k Kind) Ref() string {
	if k == KindProcessor {
		return ProcessorRef
	}
	return ConnectorRef
}

// SDKVersion returns the connector/processor SDK version pinned in kind's
// snapshot.
func (k Kind) SDKVersion() string {
	if k == KindProcessor {
		return ProcessorSDKVersion
	}
	return ConnectorSDKVersion
}

// executableFiles lists, per Kind, the snapshot-relative paths that must be
// written back out with the executable bit set. go:embed's fs.FS does not
// preserve the source tree's file mode (embedded files are exposed as
// read-only, mode 0444, regardless of what git recorded), so Extract cannot
// infer this from the embedded data itself. This list is the git ls-files
// --stage executable bit (100755) for each snapshot at its pinned ref,
// captured by hand at sync time — recheck it against `git ls-files --stage`
// on the next resync in case the upstream template adds or removes a script.
var executableFiles = map[Kind][]string{
	KindConnector: {
		filepath.Join("scripts", "bump_version.sh"),
		filepath.Join("scripts", "tag.sh"),
	},
	KindProcessor: {
		// The processor template has no scripts/ directory as of ProcessorRef.
	},
}

// FS returns the embedded snapshot for kind, rooted so paths inside it are
// relative to the template root (e.g. "go.mod", "cmd/connector/main.go")
// rather than prefixed with "connector/" or "processor/".
func FS(kind Kind) (fs.FS, error) {
	switch kind {
	case KindConnector:
		return fs.Sub(connectorFS, "connector")
	case KindProcessor:
		return fs.Sub(processorFS, "processor")
	default:
		return nil, cerrors.Errorf("template: unknown kind %q", kind)
	}
}

// Extract writes kind's embedded snapshot into destDir, which must already
// exist. It preserves the executableFiles allowlist's permissions and
// otherwise writes regular files as 0o644 and directories as 0o755 — the
// snapshot has no other special file types (no symlinks: go:embed does not
// support them). Every ".tmpl"-suffixed path in the snapshot (see
// tmplSuffix's doc) is written out with that suffix stripped, restoring the
// real filename (go.mod.tmpl -> go.mod, connector.go.tmpl -> connector.go).
//
// Extract does not itself guarantee "no partial directory on failure" —
// that discipline belongs to the caller (scaffold.Generate stages into a
// temp directory and only renames it into place once every step, including
// Extract, has succeeded).
func Extract(kind Kind, destDir string) error {
	src, err := FS(kind)
	if err != nil {
		return err
	}

	exec := make(map[string]bool, len(executableFiles[kind]))
	for _, p := range executableFiles[kind] {
		exec[p] = true
	}

	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return cerrors.Errorf("template: walking snapshot at %q: %w", path, err)
		}
		if path == "." {
			return nil
		}

		target := filepath.Join(destDir, filepath.FromSlash(strings.TrimSuffix(path, tmplSuffix)))

		if d.IsDir() {
			if mkErr := os.MkdirAll(target, 0o755); mkErr != nil {
				return cerrors.Errorf("template: creating directory %q: %w", target, mkErr)
			}
			return nil
		}

		data, readErr := fs.ReadFile(src, path)
		if readErr != nil {
			return cerrors.Errorf("template: reading %q from snapshot: %w", path, readErr)
		}

		mode := os.FileMode(0o644)
		if exec[path] {
			mode = 0o755
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return cerrors.Errorf("template: creating directory %q: %w", filepath.Dir(target), mkErr)
		}
		if writeErr := os.WriteFile(target, data, mode); writeErr != nil {
			return cerrors.Errorf("template: writing %q: %w", target, writeErr)
		}
		return nil
	})
}
