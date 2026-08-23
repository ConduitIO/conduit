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

package generate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/yaml/v3"
)

// TestTranscripts_CarryNoSecretMaterial is AC 1.21: every transcript
// committed anywhere under testdata/transcripts is scanned
// (ScanTranscriptForSecrets, redact.go) and must carry no secret material.
//
// manifest.yaml and any "<id>.missing.yaml" tombstone (transcript.go) are
// NOT parsed into a Transcript and skipped from there on — a struct-shaped
// scan is only as good as ScanTranscriptForSecrets' knowledge of
// Transcript's fields, and Manifest/Tombstone carry their own free-text-
// shaped fields (RequestOutcome.FailureReason, Tombstone.FailureCode) that
// Transcript has no field for at all, so unmarshaling either into a
// Transcript would silently scan zero of them (Transcript.Turns would just
// come back empty). Both are instead scanned as RAW TEXT via
// scanTextForSecrets directly — deliberately the one check in this package
// with no schema-shaped blind spot: it catches a secret in a field added to
// either type tomorrow even if nothing else here is ever updated for it.
// This is the regression test for the incident where a provider's raw HTTP
// error body (readErrorBody, provider/http.go) could reach
// RequestOutcome.FailureReason and then manifest.yaml completely unscanned,
// because this test used to skip manifest.yaml by name.
//
// Deliberately UNTAGGED — no `//go:build` line — so it runs in the normal
// `test` job on every PR, over WHATEVER transcripts exist today. That is
// none: A5a-3 (a later PR) is what captures the first 28 real transcripts,
// so this test's job right now is to pass cleanly on an empty corpus and
// keep failing loudly the day a captured transcript ever carries something
// it shouldn't — never to gate on the capture pipeline having run yet. A
// transcripts directory that does not exist at all is treated exactly like
// an empty one: this test proves an invariant about content, not about
// whether A5a-3 has landed.
func TestTranscripts_CarryNoSecretMaterial(t *testing.T) {
	root := "testdata/transcripts"

	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("no transcripts directory yet (testdata/transcripts) — nothing to scan")
	}

	var checked int
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %q: %w", path, err)
		}

		if d.Name() == manifestFileName || strings.HasSuffix(d.Name(), tombstoneFileSuffix) {
			checked++
			if findings := scanTextForSecrets(string(data)); len(findings) > 0 {
				t.Errorf("%s carries secret-scan findings:\n  %s", path, strings.Join(findings, "\n  "))
			}
			return nil
		}

		var tr Transcript
		if err := yaml.Unmarshal(data, &tr); err != nil {
			return fmt.Errorf("parsing %q: %w", path, err)
		}

		checked++
		if findings := ScanTranscriptForSecrets(context.Background(), tr); len(findings) > 0 {
			t.Errorf("%s carries secret-scan findings:\n  %s", path, strings.Join(findings, "\n  "))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking %q: %v", root, walkErr)
	}

	t.Logf("scanned %d committed transcript(s) under %s", checked, root)
}
