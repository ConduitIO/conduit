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
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// restoreRedactedSettings makes the API's redacted GET -> UPDATE round trip
// safe (#2913). Every API response replaces each Settings value with
// log.Redacted ("***", see pkg/http/api/toproto/redact.go), so a client that
// reads an entity and sends its settings back would otherwise overwrite every
// credential with the literal "***".
//
// Rule (security decision, 2026-10-12): a value that is exactly log.Redacted
// keeps the stored value for its key ONLY when no other setting in the same
// update changes. That is the pure round trip, and an update that changes
// nothing besides re-sending the redacted values. "Changes" means a non-"***"
// value that differs from the stored one, a key that has no stored value
// (added), or a stored key that the update leaves out (removed). If anything
// changed, every "***" in the update is refused with
// CodeRedactedSettingWithOtherChanges; the caller must send real values.
//
// Why: with a per-key rule, a caller could point a connector's host or URL at
// a server it controls, send "***" for the password, and Conduit would send
// the stored password there. The API is unauthenticated by default, so "can
// modify" would have meant "can read secrets". The remaining limitation is
// that only a pure round trip keeps secrets: to change one setting, the
// client must re-send the real value of every secret.
//
// When nothing is refused, the result is a copy of update in which every value
// that is exactly log.Redacted is replaced by the stored value for the same
// key. Any other value, including one that merely contains "***", is a real
// value and is kept as sent. Keys absent from update are not added back: an
// update replaces the whole settings map, as it always has. A setting whose
// real value is literally "***" cannot be set through an update.
//
// Precedence of refusals (the caller wraps this with the plugin check, see
// restoreRedactedSettingsForPlugin):
//
//  1. CodeRedactedSettingPluginChanged (checked by the wrapper, first)
//  2. CodeRedactedSettingWithoutStoredValue: a "***" key has no stored value;
//     keeping "nothing" would silently store "***". All such keys are named
//     in the message; ConfigPath points at the first one, under pathPrefix
//     (the JSON pointer of the settings map in the request, e.g.
//     "/config/settings").
//  3. CodeRedactedSettingWithOtherChanges: as above. The message names the
//     pointer of every "***" key; ConfigPath is the first.
//
// Neither map is mutated. It must run before the settings are validated, so
// the plugin validates the real values, not the placeholder.
func restoreRedactedSettings(stored, update map[string]string, pathPrefix string) (map[string]string, error) {
	if update == nil {
		return nil, nil
	}

	out := make(map[string]string, len(update))
	var missing, redacted []string
	changed := false // any setting other than a "***" key differs from stored
	for k, v := range update {
		if v != log.Redacted {
			out[k] = v
			if storedValue, ok := stored[k]; !ok || storedValue != v {
				changed = true // changed value or added key
			}
			continue
		}
		redacted = append(redacted, k)
		storedValue, ok := stored[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		out[k] = storedValue
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		e := conduiterr.New(CodeRedactedSettingWithoutStoredValue, fmt.Sprintf(
			"setting(s) %s are %q, the placeholder API responses use for redacted values, but have no stored value to keep",
			quoteAll(missing), log.Redacted,
		))
		e.ConfigPath = pathPrefix + "/" + escapeJSONPointer(missing[0])
		e.Suggestion = fmt.Sprintf("send the real value for these settings, or leave them out; %q only means \"keep the stored value\" for a setting that already has one", log.Redacted)
		return nil, e
	}

	if len(redacted) == 0 {
		return out, nil
	}
	for k := range stored {
		if _, ok := update[k]; !ok {
			changed = true // removed key
		}
	}
	if changed {
		// Invariant: a stored secret is never re-bound to changed settings.
		sort.Strings(redacted)
		paths := make([]string, len(redacted))
		for i, k := range redacted {
			paths[i] = pathPrefix + "/" + escapeJSONPointer(k)
		}
		e := conduiterr.New(CodeRedactedSettingWithOtherChanges, fmt.Sprintf(
			"setting(s) at %s are %q (keep the stored value), but other settings in this update change; the stored value is kept only when nothing else changes",
			quoteAll(paths), log.Redacted,
		))
		e.ConfigPath = paths[0]
		e.Suggestion = fmt.Sprintf("when changing other settings, send the real value for every setting that is %q; %q only keeps stored values in an update that changes nothing else", log.Redacted, log.Redacted)
		return nil, e
	}
	return out, nil
}

// restoreRedactedSettingsForPlugin is restoreRedactedSettings for an update
// that names a plugin. If the plugin changes (any difference in the plugin
// reference, including only its version), any "***" in update is refused
// with CodeRedactedSettingPluginChanged: a stored value, typically a
// credential entered for one plugin, is never carried to a different one.
// Real values are needed when changing the plugin.
func restoreRedactedSettingsForPlugin(storedPlugin, newPlugin string, stored, update map[string]string, pathPrefix string) (map[string]string, error) {
	if storedPlugin != newPlugin {
		if keys := redactedKeys(update); len(keys) > 0 {
			e := conduiterr.New(CodeRedactedSettingPluginChanged, fmt.Sprintf(
				"setting(s) %s are %q (keep the stored value), but the plugin changes from %q to %q; stored values are not carried to a different plugin",
				quoteAll(keys), log.Redacted, storedPlugin, newPlugin,
			))
			e.ConfigPath = pathPrefix + "/" + escapeJSONPointer(keys[0])
			e.Suggestion = "send the real value for these settings when changing the plugin"
			return nil, e
		}
	}
	return restoreRedactedSettings(stored, update, pathPrefix)
}

// RefuseRedactedSettings returns a CodeRedactedSettingWithoutStoredValue
// error if any value in settings is exactly log.Redacted ("***"), and nil
// otherwise. It is for paths that create or replace an entity from a
// document (CreateConnector, CreateProcessor, ApplyPipeline), where there is
// no stored value to keep and "***" would otherwise be stored literally.
// pathPrefix is the JSON pointer of the settings map in the request.
func RefuseRedactedSettings(settings map[string]string, pathPrefix string) error {
	_, err := restoreRedactedSettings(nil, settings, pathPrefix)
	return err
}

func redactedKeys(settings map[string]string) []string {
	var keys []string
	for k, v := range settings {
		if v == log.Redacted {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func quoteAll(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = fmt.Sprintf("%q", k)
	}
	return strings.Join(q, ", ")
}

// escapeJSONPointer escapes one reference token per RFC 6901.
func escapeJSONPointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}
