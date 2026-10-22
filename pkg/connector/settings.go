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

import (
	"github.com/conduitio/conduit/pkg/foundation/env"
)

// SettingsResolver turns the settings stored on a connector instance into the
// settings handed to the plugin's Configure call. It is the single place where
// stored (and therefore API-influenced) setting text is vetted before it
// reaches a plugin: ${VAR} expansion is policed here (see package env), and the
// engine adds per-plugin checks such as the file connector's allowed
// directories.
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
// SettingsResolver. It expands no environment variable at all, so a Service
// that was not wired by the runtime fails closed instead of exposing the whole
// process environment.
type DefaultSettingsResolver struct{}

// ResolveSettings implements SettingsResolver.
func (DefaultSettingsResolver) ResolveSettings(_ string, settings map[string]string) (map[string]string, error) {
	return env.ResolveSettings(nil, settings)
}

// EnvSettingsResolver expands ${VAR} references for the names in Allow only.
type EnvSettingsResolver struct {
	Allow *env.Allowlist
}

// ResolveSettings implements SettingsResolver.
func (r EnvSettingsResolver) ResolveSettings(_ string, settings map[string]string) (map[string]string, error) {
	return env.ResolveSettings(r.Allow, settings)
}

// resolveSettings resolves the instance's settings through its resolver.
func (i *Instance) resolveSettings(settings map[string]string) (map[string]string, error) {
	r := i.settingsResolver
	if r == nil {
		r = DefaultSettingsResolver{}
	}
	return r.ResolveSettings(i.Plugin, settings)
}

// resolveEventSettings resolves settings for a lifecycle event. nil stays nil:
// it is how the event kind (created / updated / deleted) is told apart.
func (i *Instance) resolveEventSettings(settings map[string]string) (map[string]string, error) {
	if settings == nil {
		return nil, nil
	}
	return i.resolveSettings(settings)
}
