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

package registry

// chaosHook, when non-nil, is called at named points inside Install
// (chaosPointDownloadComplete, chaosPointExtractComplete,
// chaosPointPrerenameFDOpened, chaosPointPostRenamePreManifest) — a
// test-only crash-injection mechanism. Production code (including
// cmd/conduit/root/connectors/install.go) never sets this; only
// install_test.go's chaos subprocess test binary does, via an
// environment-variable-selected point, immediately calling os.Exit(137)
// (SIGKILL's conventional exit status) to simulate a kill -9 at that exact
// point in the pipeline. See install_test.go's TestInstall_ChaosKill for
// the harness and docs/test-cases-style chaos test conventions (CLAUDE.md:
// chaos tests use SIGKILL, not SIGTERM).
var chaosHook func(point string)

const (
	chaosPointDownloadComplete      = "download-complete"
	chaosPointExtractComplete       = "extract-complete"
	chaosPointPrerenameFDOpened     = "prerename-fd-opened"
	chaosPointPostRenamePreManifest = "post-rename-pre-manifest"
)

func fireChaos(point string) {
	if chaosHook != nil {
		chaosHook(point)
	}
}
