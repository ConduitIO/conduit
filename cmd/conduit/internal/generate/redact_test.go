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
	"strings"
	"testing"

	"github.com/matryer/is"
)

// Test_ScanTextForSecrets_DetectsEveryDenyListPattern is the scanner's own
// perturbation proof, one synthetic string per pattern: each must be
// flagged, and a clean control string must not be.
func Test_ScanTextForSecrets_DetectsEveryDenyListPattern(t *testing.T) {
	is := is.New(t)

	cases := map[string]string{
		"anthropic key":   "here is my key: sk-ant-api03-FAKEFAKEFAKEFAKEFAKEFAKE123456789",
		"generic sk key":  "token=sk-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef",
		"github token":    "auth: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
		"aws access key":  "AKIAABCDEFGHIJKLMNOP is the access key",
		"pem block":       "-----BEGIN RSA PRIVATE KEY-----\nMIIB...\n-----END RSA PRIVATE KEY-----",
		"jwt":             "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"long base64 run": strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 3),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			found := scanTextForSecrets(text)
			if len(found) == 0 {
				t.Fatalf("scanTextForSecrets found nothing in a %s fixture", name)
			}
		})
	}

	is.Equal(len(scanTextForSecrets("version: \"2.2\"\npipelines:\n  - id: p\n")), 0)
}

func Test_IsPlaceholderValue(t *testing.T) {
	is := is.New(t)

	for _, v := range []string{"TODO", "todo", "TODO_REPLACE_ME", "CHANGEME", "<your-api-key>", "", "  "} {
		is.True(isPlaceholderValue(v))
	}
	for _, v := range []string{"hunter2", "AKIAABCDEFGHIJKLMNOP", "sk-ant-api03-realkeylookingvalue"} {
		is.True(!isPlaceholderValue(v))
	}
}

// Test_ScanCandidateSettingsForCredentials_FlagsNonPlaceholderCredentialKey
// pins the structural check independent of the deny-list scan: a value that
// matches NO known secret format (a plausible DSN with a plaintext password)
// still gets flagged because of the KEY it's assigned to.
func Test_ScanCandidateSettingsForCredentials_FlagsNonPlaceholderCredentialKey(t *testing.T) {
	is := is.New(t)

	candidate := "version: \"2.2\"\n" +
		"pipelines:\n" +
		"  - id: p\n" +
		"    connectors:\n" +
		"      - id: src\n" +
		"        type: source\n" +
		"        plugin: \"builtin:postgres\"\n" +
		"        settings:\n" +
		"          url: \"postgres://user:hunter2@host/db\"\n"

	found := scanCandidateSettingsForCredentials(context.Background(), candidate)
	is.True(len(found) == 0) // "url" is not a credential-shaped key

	credentialShaped := strings.Replace(candidate, "url:", "conn.password:", 1)
	found = scanCandidateSettingsForCredentials(context.Background(), credentialShaped)
	is.True(len(found) == 1)
	is.True(strings.Contains(found[0], "conn.password"))
}

// Test_ScanCandidateSettingsForCredentials_ChecksProcessorSettingsToo pins
// that a PROCESSOR's settings (pipeline-level or connector-attached) are
// checked the same way a connector's are — plan §5 says "any settings key",
// and config.Processor has its own Settings map a processor plugin (e.g. one
// calling an external enrichment API) could put a real credential in.
func Test_ScanCandidateSettingsForCredentials_ChecksProcessorSettingsToo(t *testing.T) {
	is := is.New(t)

	candidate := "version: \"2.2\"\n" +
		"pipelines:\n" +
		"  - id: p\n" +
		"    processors:\n" +
		"      - id: enrich\n" +
		"        plugin: \"custom.enrich\"\n" +
		"        settings:\n" +
		"          api_key: \"hunter2-actual-value-9f8e7d\"\n"

	found := scanCandidateSettingsForCredentials(context.Background(), candidate)
	is.True(len(found) == 1)
	is.True(strings.Contains(found[0], "processor"))
	is.True(strings.Contains(found[0], "api_key"))
}

// Test_ScanCandidateSettingsForCredentials_AcceptsPlaceholder pins that a
// credential-shaped key holding the literal TODO grounding.go instructs the
// model to emit is NOT flagged — a false positive here would make every
// clean, correctly-generated transcript fail the scan.
func Test_ScanCandidateSettingsForCredentials_AcceptsPlaceholder(t *testing.T) {
	is := is.New(t)

	candidate := "version: \"2.2\"\n" +
		"pipelines:\n" +
		"  - id: p\n" +
		"    connectors:\n" +
		"      - id: src\n" +
		"        type: source\n" +
		"        plugin: \"builtin:postgres\"\n" +
		"        settings:\n" +
		"          password: TODO\n"

	found := scanCandidateSettingsForCredentials(context.Background(), candidate)
	is.Equal(len(found), 0)
}

// Test_ScanCandidateSettingsForCredentials_UnparseableTextYieldsNoFindings
// pins that narration-only or malformed text is skipped by this check rather
// than erroring — the deny-list scan (checked separately) is what still
// covers raw, unparseable text.
func Test_ScanCandidateSettingsForCredentials_UnparseableTextYieldsNoFindings(t *testing.T) {
	is := is.New(t)
	found := scanCandidateSettingsForCredentials(context.Background(), "I'm sorry, I cannot help with that request.")
	is.Equal(len(found), 0)
}

// harmlessFiller returns n bytes of filler text that trips none of
// secretPatterns — in particular the "long base64 run" pattern, which a bare
// strings.Repeat("a", n) would itself match (lowercase letters are valid
// base64 alphabet). A newline every other byte breaks up any run before it
// reaches the pattern's 80-character floor.
func harmlessFiller(n int) string {
	var b strings.Builder
	b.Grow(n)
	for b.Len() < n {
		b.WriteString("a\n")
	}
	return b.String()[:n]
}

// Test_ScanTranscriptForSecrets_EnforcesTheSizeCap pins MaxTurnCompletionBytes
// as a hard cap: a turn one byte over it is flagged, one byte under is not.
func Test_ScanTranscriptForSecrets_EnforcesTheSizeCap(t *testing.T) {
	is := is.New(t)

	within := Transcript{
		RequestID: "req",
		Turns:     []Turn{{N: 1, CompletionText: harmlessFiller(MaxTurnCompletionBytes)}},
	}
	is.Equal(len(ScanTranscriptForSecrets(context.Background(), within)), 0)

	over := Transcript{
		RequestID: "req",
		Turns:     []Turn{{N: 1, CompletionText: harmlessFiller(MaxTurnCompletionBytes + 1)}},
	}
	found := ScanTranscriptForSecrets(context.Background(), over)
	is.True(len(found) == 1)
	is.True(strings.Contains(found[0], "exceeds"))
}

// Test_ScanTranscriptForSecrets_CleanTranscriptHasNoFindings is the negative
// control every perturbation test in this file is contrasted against: a
// realistic, well-formed transcript scans clean.
func Test_ScanTranscriptForSecrets_CleanTranscriptHasNoFindings(t *testing.T) {
	is := is.New(t)

	tr := Transcript{
		RequestID: "postgres-cdc-to-kafka-filtered",
		Turns: []Turn{{
			N: 1,
			CompletionText: "version: \"2.2\"\n" +
				"pipelines:\n" +
				"  - id: p\n" +
				"    connectors:\n" +
				"      - id: src\n" +
				"        type: source\n" +
				"        plugin: \"builtin:postgres\"\n" +
				"        settings:\n" +
				"          url: TODO\n",
		}},
	}
	is.Equal(len(ScanTranscriptForSecrets(context.Background(), tr)), 0)
}

// Test_ScanTranscriptForSecrets_PlantedSecretIsCaught is the scanner-level
// perturbation proof (as opposed to the pattern-level one above): a
// synthetic Anthropic key planted in a turn's completion text — the exact
// shape a captured transcript could carry if capture forgot to redact
// narration — must be caught, naming which turn.
func Test_ScanTranscriptForSecrets_PlantedSecretIsCaught(t *testing.T) {
	is := is.New(t)

	tr := Transcript{
		RequestID: "req",
		Turns: []Turn{{
			N: 2,
			CompletionText: "Sure, here is the config. By the way my key is " +
				"sk-ant-api03-FAKEFAKEFAKEFAKEFAKEFAKE123456789 in case that helps.\n" +
				"```yaml\nversion: \"2.2\"\n```\n",
		}},
	}

	found := ScanTranscriptForSecrets(context.Background(), tr)
	is.True(len(found) == 1)
	is.True(strings.Contains(found[0], "req"))
	is.True(strings.Contains(found[0], "turn 2"))
	is.True(strings.Contains(found[0], "Anthropic API key"))
	is.True(!strings.Contains(found[0], "sk-ant-api03")) // the finding never echoes the matched secret itself
}
