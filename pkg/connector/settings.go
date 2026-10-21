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

package connector

// SettingsResolver turns the settings stored on a connector instance into the
// settings handed to the plugin's Configure call. It is the single place where
// stored (and therefore API-influenced) setting text is vetted before it
// reaches a plugin; the engine uses it to confine the file connector to the
// operator's allowed directories.
//
// Tier 1: this is on the connector configure path. An implementation must
// fail closed, return a new map, and must not modify its input.
type SettingsResolver interface {
	// ResolveSettings returns the settings for the plugin named plugin (the
	// instance's Plugin field, for example "builtin:file@v0.10.9"). A refusal
	// must be a coded *conduiterr.ConduitError.
	ResolveSettings(plugin string, settings map[string]string) (map[string]string, error)
}

// DefaultSettingsResolver is used when the Service is built without a
// SettingsResolver. It passes the settings through unchanged.
type DefaultSettingsResolver struct{}

// ResolveSettings implements SettingsResolver.
func (DefaultSettingsResolver) ResolveSettings(_ string, settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		out[k] = v
	}
	return out, nil
}

// resolveSettings resolves the instance's settings through its resolver.
func (i *Instance) resolveSettings(settings map[string]string) (map[string]string, error) {
	r := i.settingsResolver
	if r == nil {
		r = DefaultSettingsResolver{}
	}
	return r.ResolveSettings(i.Plugin, settings)
}
