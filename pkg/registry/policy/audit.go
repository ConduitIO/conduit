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

package policy

import (
	"os"
	"path/filepath"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// UnsignedInstallEvent is one line of the append-only
// <basePath>/registry/unsigned-installs.log (plan-v2 §6): the durable,
// tamper-evident-by-append-only record of every install that proceeded
// WITHOUT signature/provenance verification. This — NOT manifest.json's
// per-entry Signed/AllowUnsigned fields, which are locally rewritable by
// anyone with filesystem access — is the source `audit`/`list --installed`
// (PR-4) read for "was this connector ever installed unsigned" (plan-v2 §13
// P2 nit).
type UnsignedInstallEvent struct {
	Connector      string    `json:"connector"`
	Version        string    `json:"version"`
	ResolvedDigest string    `json:"resolvedDigest"`
	Operator       string    `json:"operator"`
	Timestamp      time.Time `json:"timestamp"`
	// Context is "tty" (interactive typed confirmation) or
	// "non-interactive-env" (CONDUIT_ALLOW_UNSIGNED_INSTALL=I_UNDERSTAND in
	// a non-interactive context) — the two Decide branches that can ever
	// produce Allowed() == true.
	Context string `json:"context"`
}

// AppendUnsignedInstallEvent appends ev as one JSON line to path, creating
// the file (and its parent directory) with 0600 permissions if it doesn't
// exist yet. Append-only by construction (os.O_APPEND, never truncate or
// rewrite) — this is what makes the log tamper-evident-by-omission: an
// attacker with filesystem access can delete or truncate it, but cannot
// selectively edit a past entry without leaving the file's own append
// ordering visibly disturbed to an operator who diffs it against a backup.
func AppendUnsignedInstallEvent(path string, ev UnsignedInstallEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return cerrors.Errorf("could not marshal unsigned-install audit event: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return cerrors.Errorf("could not create directory for unsigned-install audit log %q: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return cerrors.Errorf("could not open unsigned-install audit log %q: %w", path, err)
	}
	defer f.Close() // best-effort; the write/sync below already reports any real error

	if _, err := f.Write(data); err != nil {
		return cerrors.Errorf("could not append to unsigned-install audit log %q: %w", path, err)
	}
	return f.Sync()
}
