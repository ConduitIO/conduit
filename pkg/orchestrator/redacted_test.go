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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestRestoreRedactedSettings(t *testing.T) {
	stored := map[string]string{"password": "s3cret", "host": "db", "token": "t0k"}

	// "***" keeps the stored value only when nothing else in the update
	// changes (security decision, 2026-10-12).
	keptCases := []struct {
		name   string
		update map[string]string
		want   map[string]string
	}{
		{
			name:   "pure round trip keeps every stored value",
			update: map[string]string{"password": log.Redacted, "host": log.Redacted, "token": log.Redacted},
			want:   stored,
		},
		{
			name:   "several *** alongside an unchanged non-secret field",
			update: map[string]string{"password": log.Redacted, "token": log.Redacted, "host": "db"},
			want:   stored,
		},
		{
			name:   "no *** at all: every value is real and replaces",
			update: map[string]string{"password": "new", "host": "other", "port": "5432"},
			want:   map[string]string{"password": "new", "host": "other", "port": "5432"},
		},
		{
			name:   "only an exact match is the placeholder",
			update: map[string]string{"password": " ***", "host": "****", "token": "a***"},
			want:   map[string]string{"password": " ***", "host": "****", "token": "a***"},
		},
		{
			name:   "empty update",
			update: map[string]string{},
			want:   map[string]string{},
		},
		{
			name:   "nil update stays nil",
			update: nil,
			want:   nil,
		},
	}
	for _, tc := range keptCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			before := map[string]string{}
			for k, v := range tc.update {
				before[k] = v
			}

			got, err := restoreRedactedSettings(stored, tc.update, "/config/settings")
			is.NoErr(err)
			is.Equal(got, tc.want)
			if tc.update != nil {
				is.Equal(tc.update, before) // the caller's map is not mutated
			}
		})
	}

	refusedCases := []struct {
		name      string
		update    map[string]string
		wantPaths []string // every *** key's pointer, sorted
	}{
		{
			// The exfiltration case: same plugin, host repointed, password "***".
			name:      "changed host with *** password",
			update:    map[string]string{"password": log.Redacted, "token": log.Redacted, "host": "evil.example"},
			wantPaths: []string{"/config/settings/password", "/config/settings/token"},
		},
		{
			name:      "a changed value that merely contains ***",
			update:    map[string]string{"password": log.Redacted, "host": "a***b", "token": log.Redacted},
			wantPaths: []string{"/config/settings/password", "/config/settings/token"},
		},
		{
			name:      "added key",
			update:    map[string]string{"password": log.Redacted, "host": "db", "token": log.Redacted, "port": "5432"},
			wantPaths: []string{"/config/settings/password", "/config/settings/token"},
		},
		{
			name:      "removed key (a real key left out)",
			update:    map[string]string{"password": log.Redacted, "token": log.Redacted},
			wantPaths: []string{"/config/settings/password", "/config/settings/token"},
		},
		{
			name:      "removed key (only *** keys remain)",
			update:    map[string]string{"password": log.Redacted},
			wantPaths: []string{"/config/settings/password"},
		},
	}
	for _, tc := range refusedCases {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			is := is.New(t)
			got, err := restoreRedactedSettings(stored, tc.update, "/config/settings")
			is.True(got == nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeRedactedSettingWithOtherChanges)
			is.Equal(ce.ConfigPath, tc.wantPaths[0]) // ConfigPath is the first *** key
			for _, p := range tc.wantPaths {
				is.True(strings.Contains(ce.Message, p)) // the message names every *** key's path
			}
			is.True(strings.Contains(ce.Suggestion, "real"))
			is.True(!strings.Contains(ce.Message, "s3cret")) // never echoes a stored value
		})
	}
}

func TestRestoreRedactedSettingsForPlugin(t *testing.T) {
	stored := map[string]string{"password": "s3cret", "host": "db"}

	t.Run("same plugin, pure round trip restores", func(t *testing.T) {
		is := is.New(t)
		got, err := restoreRedactedSettingsForPlugin("builtin:pg", "builtin:pg", stored,
			map[string]string{"password": log.Redacted, "host": log.Redacted}, "/config/settings")
		is.NoErr(err)
		is.Equal(got, stored)
	})

	t.Run("same plugin, host changed with *** password is refused", func(t *testing.T) {
		is := is.New(t)
		got, err := restoreRedactedSettingsForPlugin("builtin:pg", "builtin:pg", stored,
			map[string]string{"password": log.Redacted, "host": "evil"}, "/config/settings")
		is.True(got == nil)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeRedactedSettingWithOtherChanges)
		is.Equal(ce.ConfigPath, "/config/settings/password")
	})

	for _, newPlugin := range []string{"builtin:http", "builtin:pg@v0.2.0", ""} {
		t.Run("plugin change refuses *** ("+newPlugin+")", func(t *testing.T) {
			is := is.New(t)
			got, err := restoreRedactedSettingsForPlugin("builtin:pg", newPlugin, stored,
				map[string]string{"password": log.Redacted, "host": log.Redacted, "port": "1"}, "/dlq/settings")
			is.True(got == nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeRedactedSettingPluginChanged)
			is.True(strings.Contains(ce.Message, `"host", "password"`))
			is.True(strings.Contains(ce.Message, `"builtin:pg"`))
			is.Equal(ce.ConfigPath, "/dlq/settings/host")
		})
	}

	// Precedence, most fundamental problem first:
	//  1. the plugin changed        -> plugin_changed
	//  2. a *** key has no stored value -> without_stored_value
	//  3. any other setting changed -> with_other_changes
	t.Run("precedence", func(t *testing.T) {
		code := func(storedPlugin, newPlugin string, update map[string]string) conduiterr.Code {
			t.Helper()
			_, err := restoreRedactedSettingsForPlugin(storedPlugin, newPlugin, stored, update, "/config/settings")
			ce, ok := conduiterr.Get(err)
			if !ok {
				t.Fatalf("want a ConduitError, got %v", err)
			}
			return ce.Code
		}
		is := is.New(t)

		// All three problems at once: plugin changed wins.
		all := map[string]string{"nostored": log.Redacted, "password": log.Redacted, "host": "evil"}
		is.Equal(code("builtin:pg", "builtin:http", all), CodeRedactedSettingPluginChanged)
		// Same plugin: no stored value wins over other changes.
		is.Equal(code("builtin:pg", "builtin:pg", all), CodeRedactedSettingWithoutStoredValue)
		// Same plugin, every *** has a stored value, something else changed.
		is.Equal(code("builtin:pg", "builtin:pg", map[string]string{"password": log.Redacted, "host": "evil"}), CodeRedactedSettingWithOtherChanges)
		// Plugin changed beats other changes even with stored values.
		is.Equal(code("builtin:pg", "builtin:http", map[string]string{"password": log.Redacted, "host": "evil"}), CodeRedactedSettingPluginChanged)
		// No *** at all: nothing to refuse, whatever changed.
		_, err := restoreRedactedSettingsForPlugin("builtin:pg", "builtin:pg", stored, map[string]string{"host": "evil"}, "/config/settings")
		is.NoErr(err)
	})

	t.Run("plugin change with real values is fine", func(t *testing.T) {
		is := is.New(t)
		got, err := restoreRedactedSettingsForPlugin("builtin:pg", "builtin:http", stored,
			map[string]string{"url": "http://x"}, "/config/settings")
		is.NoErr(err)
		is.Equal(got, map[string]string{"url": "http://x"})
	})
}

func TestRefuseRedactedSettings(t *testing.T) {
	is := is.New(t)
	is.NoErr(RefuseRedactedSettings(nil, "/config/settings"))
	is.NoErr(RefuseRedactedSettings(map[string]string{"a": "a***"}, "/config/settings"))

	err := RefuseRedactedSettings(map[string]string{"a": "x", "b": log.Redacted}, "/connectors/2/settings")
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeRedactedSettingWithoutStoredValue)
	is.Equal(ce.ConfigPath, "/connectors/2/settings/b")
}

func TestRestoreRedactedSettings_NoStoredValue(t *testing.T) {
	is := is.New(t)
	stored := map[string]string{"host": "db"}
	update := map[string]string{"host": log.Redacted, "z/key": log.Redacted, "a.key": log.Redacted}

	got, err := restoreRedactedSettings(stored, update, "/dlq/settings")
	is.True(got == nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeRedactedSettingWithoutStoredValue)
	is.True(strings.Contains(ce.Message, `"a.key", "z/key"`)) // every key, sorted
	is.Equal(ce.ConfigPath, "/dlq/settings/a.key")            // the first one
	is.True(ce.Suggestion != "")

	// RFC 6901 escaping of the pointer token.
	_, err = restoreRedactedSettings(nil, map[string]string{"a/b~c": log.Redacted}, "/config/settings")
	ce, _ = conduiterr.Get(err)
	is.Equal(ce.ConfigPath, "/config/settings/a~1b~0c")
}
