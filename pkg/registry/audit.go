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

import (
	"os"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// AuditEvent is one append-only entry in <connectors.path>/.registry/audit.jsonl.
//
// Signed/VerifiedIdentity/AllowUnsigned mirror ManifestEntry's own fields:
// present now (PR-0/PR-1) even though every event this build can produce has
// Signed: false, VerifiedIdentity: "", AllowUnsigned: false (see
// manifest.go's ManifestEntry doc — the same reasoning applies here).
type AuditEvent struct {
	Event            string    `json:"event"` // "connector_install" or "connector_uninstall" (PR-4)
	Connector        string    `json:"connector"`
	Version          string    `json:"version"`
	Digest           string    `json:"digest"` // "sha256:<hex>"
	Operator         string    `json:"operator,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	Signed           bool      `json:"signed"`
	VerifiedIdentity string    `json:"verifiedIdentity"`
	AllowUnsigned    bool      `json:"allowUnsigned"`
	// Forced is set on an "connector_uninstall" event when --force overrode
	// an in-use refusal (PR-4, step5 §2 step 8). Always false/omitted for
	// "connector_install" events.
	Forced bool `json:"forced,omitempty"`
}

// AppendAuditEvent appends ev as one JSON line to path, creating the file
// (and its parent directory) if needed.
//
// Invariant: callers must only append an audit event AFTER the install it
// describes has already durably completed (binary renamed into place,
// manifest written) — install.go's call site does this last, deliberately,
// so the audit trail never claims an install happened before it actually
// did.
//
// A single O_APPEND write of this size is atomic at the OS level — no
// temp-file-plus-rename dance is needed the way manifest.go's SaveManifest
// needs one for a whole-file rewrite — but Sync is still called explicitly
// to force the append to durable storage before Install reports success to
// its caller.
func AppendAuditEvent(path string, ev AuditEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return cerrors.Errorf("could not marshal audit event: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return cerrors.Errorf("could not open audit log %q: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return cerrors.Errorf("could not append to audit log %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return cerrors.Errorf("could not sync audit log %q: %w", path, err)
	}
	return nil
}
