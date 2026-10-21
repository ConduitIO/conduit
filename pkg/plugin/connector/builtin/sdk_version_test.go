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

package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// sdkTombFixVersion is the first conduit-connector-sdk version whose
// sourcePluginAdapter.Run cannot panic with "tomb.Go called after all
// goroutines terminated" when a source's first Read fails immediately
// (conduit-connector-sdk#429). v0.17.0 and v0.17.1 do not have the fix.
const sdkTombFixVersion = "v0.17.2-0.20261019183849-a12df1f1f592"

// TestConnectorSDKHasRunTombFix pins the SDK at or above the tomb.Go fix.
//
// The panic itself is a scheduling race inside the SDK, so the tests that hit
// it (TestAmbientPolicy_RunStreamRefusalIsCoded and siblings) only fail some of
// the time. #3208 bumped the SDK to the fix; #3192, merged a minute later,
// resolved its go.mod against the older base and put v0.17.0 back, and the
// flake returned with nothing failing deterministically. This test fails on
// every run if go.mod requires a version below the fix again.
//
// It reads go.mod rather than the binary's build info because go test binaries
// do not record their module dependencies. The version go.mod requires is a
// floor for the version the build selects, so checking it is sufficient.
func TestConnectorSDKHasRunTombFix(t *testing.T) {
	const path = "github.com/conduitio/conduit-connector-sdk"

	gomod := conduitGoMod(t)
	data, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse(gomod, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Replace {
		if r.Old.Path == path {
			t.Skipf("%s is replaced by %s %s; cannot check its version", path, r.New.Path, r.New.Version)
		}
	}
	for _, r := range f.Require {
		if r.Mod.Path != path {
			continue
		}
		if semver.Compare(r.Mod.Version, sdkTombFixVersion) < 0 {
			t.Fatalf("go.mod requires %s %s, older than %s: source Run can panic with \"tomb.Go called after all goroutines terminated\" when the first Read fails (conduit-connector-sdk#429)", path, r.Mod.Version, sdkTombFixVersion)
		}
		return
	}
	t.Fatalf("go.mod does not require %s", path)
}

// conduitGoMod walks up from the package directory to the root go.mod.
func conduitGoMod(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(p); err == nil && modfile.ModulePath(data) == "github.com/conduitio/conduit" {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("root go.mod of github.com/conduitio/conduit not found")
		}
		dir = parent
	}
}
