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
