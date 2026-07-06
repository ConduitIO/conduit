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

package log

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// Sentinel secret values. If any of these ever show up in a log buffer in
// this file's tests, redaction failed to redact.
const (
	urlSentinel      = "postgres://u:S3CR3T_SENTINEL_9f3a@h/db"
	passwordSentinel = "PW_SENTINEL_9f3a"

	// urlKey avoids repeating the "url" string literal (goconst); it's also
	// one of the entries in sensitiveKeyNeedles, exercised by TestIsSensitive.
	urlKey = "url"
)

// secretFixture returns a connector config carrying the kind of values real
// connectors put in config.Config: a DB URL with an embedded password and a
// SASL credential, plus one clearly non-secret value so the test can also
// confirm RedactAll's interim "redact everything" behavior redacts values
// that a keyed-on-sensitivity version would have left visible.
func secretFixture() config.Config {
	return config.Config{
		urlKey:          urlSentinel,
		"sasl.password": passwordSentinel,
		"topic":         "orders", // not a secret, still redacted (interim behavior)
	}
}

// TestRedactAll_NeverLeaksSentinels is the secrets-redaction regression test
// required by the v0.16.1 execution plan (§1.1): it drives RedactAll with a
// logger writing to an in-memory buffer and asserts the sentinel secret
// values never appear in the output, Redacted does appear, and every key
// stays visible.
func TestRedactAll_NeverLeaksSentinels(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	logger := New(zerolog.New(&buf))

	cfg := secretFixture()
	logger.Info(context.Background()).Any("config", RedactAll(cfg)).Msg("calling Configure")

	got := buf.String()

	is.True(!strings.Contains(got, urlSentinel))      // url sentinel must never appear in log output
	is.True(!strings.Contains(got, passwordSentinel)) // password sentinel must never appear in log output

	is.True(strings.Contains(got, Redacted)) // redacted marker must appear

	// Keys are not secret and stay visible so operators can still tell which
	// parameters were set.
	is.True(strings.Contains(got, `"`+urlKey+`":"***"`))
	is.True(strings.Contains(got, `"sasl.password":"***"`))
	is.True(strings.Contains(got, `"topic":"***"`)) // interim behavior: redacts non-secret keys too
}

// TestRedactAll_EmptyAndNilConfig verifies RedactAll doesn't panic or write
// anything odd for the empty/nil edge cases (a connector with no settings,
// or a lifecycle event with a nil ConfigBefore/ConfigAfter).
func TestRedactAll_EmptyAndNilConfig(t *testing.T) {
	is := is.New(t)

	testCases := []config.Config{nil, {}}
	for _, cfg := range testCases {
		var buf bytes.Buffer
		logger := New(zerolog.New(&buf))
		logger.Info(context.Background()).Any("config", RedactAll(cfg)).Msg("calling Configure")

		got := buf.String()
		is.True(strings.Contains(got, `"config":{}`))
	}
}

// TestRedactedConfig_KeysSortedDeterministic checks that output is
// deterministic across repeated calls with the same input, which matters for
// log-diffing and for the test assertions above to be reliable (map
// iteration order is randomized in Go, MarshalZerologObject must not depend
// on it).
func TestRedactedConfig_KeysSortedDeterministic(t *testing.T) {
	is := is.New(t)
	cfg := config.Config{"z": "1", "a": "2", "m": "3"}

	var first string
	for i := 0; i < 5; i++ {
		var buf bytes.Buffer
		logger := New(zerolog.New(&buf))
		logger.Info(context.Background()).Any("config", RedactAll(cfg)).Msg("")
		if i == 0 {
			first = buf.String()
			continue
		}
		is.Equal(first, buf.String())
	}
	is.True(strings.Contains(first, `"config":{"a":"***","m":"***","z":"***"}`))
}

// TestIsSensitive exercises the name heuristic on its own. isSensitive is not
// reached by RedactAll today (RedactAll always passes Params as nil, which
// redacts unconditionally) - this test exists so the heuristic is verified
// ahead of the follow-up that wires a real Params-based check into
// RedactedConfig.
func TestIsSensitive(t *testing.T) {
	testCases := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"db.password", true},
		{"PASSWORD", true},
		{"sasl.password", true},
		{"sasl.mechanism", true}, // heuristic is coarse: matches on "sasl", not just credential sub-keys
		{"passphrase", true},
		{"secret", true},
		{"client_secret", true},
		{"token", true},
		{"access_token", true},
		{"credential", true},
		{"credentials.path", true},
		{"private_key", true},
		{"auth", true},
		{"authorization", true},
		{"apikey", true},
		{"api_key", true},
		{"aws.access_key_id", true},
		{"dsn", true},
		{urlKey, true},
		{"connection_string", true},
		{"topic", false},
		{"batch.size", false},
		{"format", false},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			is := is.New(t)
			is.Equal(tc.want, isSensitive(nil, tc.key))
		})
	}
}

// TestRedactedConfig_ParamsNonNilStillRedactsUnknownSensitiveKeys documents
// today's behavior for a caller that does pass Params directly (bypassing
// RedactAll): a key not present in Params still gets redacted if the name
// heuristic flags it, and a key present in Params is only ever left visible
// if isSensitiveParameter says so - which is always false until the
// conduit-commons follow-up lands (see isSensitiveParameter's doc comment),
// so today passing a non-nil Params is not actually distinguishable from
// RedactAll for any key the heuristic doesn't recognize as sensitive... IT
// STILL REDACTS non-sensitive-looking keys as long as Params is nil; this
// test is only meaningful once isSensitiveParameter is wired up, but is
// included so a future change to isSensitiveParameter gets a failing test
// instead of silent behavior drift.
func TestRedactedConfig_ParamsNonNilStillRedactsUnknownSensitiveKeys(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	logger := New(zerolog.New(&buf))

	cfg := config.Config{"sasl.password": passwordSentinel, "topic": "orders"}
	rc := RedactedConfig{Params: config.Parameters{}, Config: cfg}
	logger.Info(context.Background()).Any("config", rc).Msg("")

	got := buf.String()
	is.True(!strings.Contains(got, passwordSentinel))
	is.True(strings.Contains(got, `"sasl.password":"***"`))
	// "topic" is not recognized as sensitive by the name heuristic and
	// Params is empty (no override), so today it is printed verbatim - this
	// is the intended keyed-on-sensitivity behavior for the future, exercised
	// here only because isSensitive/isSensitiveParameter already exist; the
	// RedactAll path used at every real call site does not take this branch.
	is.True(strings.Contains(got, `"topic":"orders"`))
}
