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

package storecompat

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
)

// AssertState checks that db holds exactly the fixture state, with srcPos
// as the source connector's position.
func AssertState(t *testing.T, ctx context.Context, db database.DB, srcPos opencdc.Position) {
	t.Helper()
	is := is.New(t)

	pipelines, err := pipeline.NewStore(db).GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(pipelines), len(expectedPipelines()))
	for id, want := range expectedPipelines() {
		got, ok := pipelines[id]
		if !ok {
			t.Fatalf("pipeline %q missing after reopen", id)
		}
		is.Equal(got.GetStatus(), want.GetStatus())
		is.Equal(got, want)
	}

	connectors, err := connector.NewStore(db, log.Nop()).GetAll(ctx)
	is.NoErr(err)
	wantConns := ExpectedConnectors(srcPos)
	is.Equal(len(connectors), len(wantConns))
	for id, want := range wantConns {
		got, ok := connectors[id]
		if !ok {
			t.Fatalf("connector %q missing after reopen", id)
		}
		is.Equal(got, want)
	}

	processors, err := processor.NewStore(db).GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(processors), len(expectedProcessors()))
	for id, want := range expectedProcessors() {
		got, ok := processors[id]
		if !ok {
			t.Fatalf("processor %q missing after reopen", id)
		}
		is.Equal(got, want)
	}
}

// CopyFixture copies a fixture directory into a temp dir so the committed
// files are never opened for writing.
func CopyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path) //nolint:gosec // test helper: path comes from walking a committed fixture dir
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // test helper: target is inside t.TempDir()
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", src, err)
	}
	return dst
}

// GoListVersion reports the version of module this build selects, for
// the test log only. Test binaries carry no dependency build info, so ask
// the go command; an error is reported in place of the version.
func GoListVersion(ctx context.Context, module string) string {
	out, err := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}", module).Output()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}
