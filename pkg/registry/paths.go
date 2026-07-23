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

import "path/filepath"

// registryDirName is the dot-prefixed subdirectory of --connectors.path that
// holds every registry-owned file: manifest.json, locks/, staging/, and
// audit.jsonl. Dot-prefixed so pkg/plugin/connector/standalone/registry.go's
// directory scanner — which already skips every directory entry outright,
// not just dot-prefixed ones, then attempts to dispense every remaining
// entry as a connector plugin binary — never treats this bookkeeping as a
// candidate connector.
const registryDirName = ".registry"

// registryDir returns <connectorsPath>/.registry.
func registryDir(connectorsPath string) string {
	return filepath.Join(connectorsPath, registryDirName)
}

// manifestPath returns <connectorsPath>/.registry/manifest.json.
func manifestPath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "manifest.json")
}

// auditLogPath returns <connectorsPath>/.registry/audit.jsonl.
func auditLogPath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "audit.jsonl")
}

// stagingRootPath returns <connectorsPath>/.registry/staging — the parent
// of every per-install staging directory (install.go creates one temp
// subdirectory per Install call via os.MkdirTemp under this root).
func stagingRootPath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "staging")
}

// locksDirPath returns <connectorsPath>/.registry/locks.
func locksDirPath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "locks")
}

// indexStatePath returns <connectorsPath>/.registry/index-state.json — the
// persisted rollback high-water mark (index.State) TrustedVerifier.VerifyIndex
// reads/writes. Lives alongside manifest.json/audit.jsonl under the same
// .registry convention (PR-0/PR-1 already established this — plan-v2's
// illustrative "<basePath>/registry/state.json" is superseded here for
// consistency with the rest of this package's on-disk layout, flagged in
// this PR's description).
func indexStatePath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "index-state.json")
}

// IndexStatePath is indexStatePath, exported for
// cmd/conduit/root/connectors/install.go — the one production call site
// that constructs a TrustedVerifier and must agree with Install's own
// internal path convention without duplicating it.
func IndexStatePath(connectorsPath string) string { return indexStatePath(connectorsPath) }

// unsignedInstallsLogPath returns
// <connectorsPath>/.registry/unsigned-installs.log — the append-only audit
// trail policy.AppendUnsignedInstallEvent writes to (plan-v2 §6, §13 P2
// nit). Same directory-convention note as indexStatePath.
func unsignedInstallsLogPath(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "unsigned-installs.log")
}
