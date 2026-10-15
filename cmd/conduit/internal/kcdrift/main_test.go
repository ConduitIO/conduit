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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules/drift"
	"github.com/matryer/is"
)

const testSnap = "scope: shared\nupstream: {name: kafka, version: \"4.2\"}\nsources: [x]\ncheck:\n  site: kafka\n  pages: [p]\nkeys:\n  - {key: k, default: \"1\"}\n"

func pageWith(def string) []byte {
	return []byte(`<h4><a id="k"></a>k</h4><table><tr><th>Default:</th><td>` + def + `</td></tr></table>`)
}

func TestRun(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	is.NoErr(os.WriteFile(filepath.Join(dir, "shared.keys.yaml"), []byte(testSnap), 0o600))
	served := map[string][]byte{drift.KafkaPageURL(drift.Version{Major: 4, Minor: 2}, "p"): pageWith("1")}
	fetch := func(_ context.Context, url string) ([]byte, error) {
		if b, ok := served[url]; ok {
			return b, nil
		}
		return nil, drift.ErrNotPublished
	}
	report, failed, err := run(context.Background(), dir, fetch)
	is.NoErr(err)
	is.True(!failed)
	is.True(strings.Contains(report, "`shared`: matches 4.2"))

	served[drift.KafkaPageURL(drift.Version{Major: 4, Minor: 3}, "p")] = pageWith("2")
	report, failed, err = run(context.Background(), dir, fetch)
	is.NoErr(err)
	is.True(failed) // drift fails the job
	is.True(strings.Contains(report, "upstream 4.3: k: default 2, snapshot 1"))

	broken := func(context.Context, string) ([]byte, error) { return nil, cerrors.New("dns") }
	_, _, err = run(context.Background(), dir, broken)
	is.True(err != nil) // unreadable docs are not "no drift"

	_, _, err = run(context.Background(), t.TempDir(), fetch)
	is.True(err != nil) // no snapshots
}
