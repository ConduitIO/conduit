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
	// chaosPointIndexStateBeforeWrite fires in TrustedVerifier.VerifyIndex
	// (trustverifier.go) immediately before index.SaveState persists the
	// new rollback high-water mark — PR-2's own kill-mid-write chaos point
	// (plan-v2 §15.3), analogous in style to the four install-pipeline
	// points above: injected between "computed what to write" and "make it
	// durable", not literally mid-syscall (the durability primitive itself,
	// pkg/foundation/atomicfile.WriteFile's temp+rename, is what actually
	// provides the no-torn-file guarantee; this point proves a kill here
	// leaves the OLD index-state.json — or none — completely intact, and
	// that a subsequent VerifyIndex call still works correctly afterward).
	chaosPointIndexStateBeforeWrite = "index-state-before-write"
)

func fireChaos(point string) {
	if chaosHook != nil {
		chaosHook(point)
	}
}
