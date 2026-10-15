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

// Package secretref resolves settings references (`${VAR}` templates) from
// the engine's environment at the moment a plugin is configured.
//
// # Why this exists
//
// Up to v0.20 the pipeline config parser expanded `${VAR}` while parsing and
// the expanded value was stored in the database, so a secret passed through
// the environment ended up in plaintext in badger, SQLite or Postgres and in
// every backup (#2988). From v0.21 a settings value of a file-provisioned
// connector, processor or DLQ that contains a reference is stored as its
// template, in a SettingRefs map next to Settings, and is resolved here, into
// a transient map, only when a plugin call needs it. See
// docs/design-documents/20261014-secrets-at-rest.md.
//
// # Core types
//
//   - Resolve merges Settings with resolved SettingRefs and reports every
//     variable that was unset. It does not decide what an unset variable
//     means: the caller warns or fails (design section 5).
//   - Resolved is the result. Its String, GoString, Format, MarshalJSON,
//     MarshalYAML, LogValue (slog) and MarshalZerologObject print "***" for
//     every value. That covers fmt, goccy/go-json, encoding/json,
//     conduitio/yaml, slog and zerolog when they are handed the Resolved
//     value itself, an exported field holding it, or a pointer to either.
//     It does not cover a Resolved held in an unexported struct field: fmt
//     cannot call methods there and prints the raw map. Hence rule R1b
//     below. The plugin boundary converts with an explicit
//     map[string]string(...).
//   - IsReference is the grammar: a value is a reference exactly when
//     os.Expand would call its mapping function for it.
//   - CodeEnvUnset and NewUnsetError are the coded error for an unset
//     variable. They name the variable, the entity and the settings key,
//     never a value.
//
// # Invariants
//
//   - R1: a Resolved map is a local variable of a plugin call. It is never
//     assigned to a stored entity (Instance.Config, LastActiveConfig, a DLQ),
//     a logger field or an error. The connector persister re-encodes
//     Instance.Config on every position flush, so one such assignment would
//     write the secret to disk within seconds.
//   - R1b: never hold a Resolved in an unexported struct field, and never
//     convert it back to map[string]string except in the plugin request
//     itself. Both bypass the redaction above.
//   - Nothing in this package logs, and no error it returns contains a
//     resolved value or a template.
//   - With every referenced variable set, Resolve produces exactly what
//     os.ExpandEnv produced for the same text in v0.20 (FuzzResolve).
//
// # Contract for the config parser (implementation PR 2)
//
// os.Expand rewrites some text without calling its mapper: "x${}y" becomes
// "xy" and "x${ay" becomes "xay", and IsReference is false for both. So
// "every config means what it meant in v0.20" holds only if the parser keeps
// two rules: a value for which IsReference is true is stored verbatim in
// SettingRefs; any other value is stored in Settings as os.ExpandEnv(value),
// exactly as v0.20 stored it, never as the raw text. Under those rules,
// storing then resolving any value equals os.ExpandEnv of it
// (FuzzStoreThenResolve).
//
// # Relationships
//
// The stored fields live in pkg/connector (Config.SettingRefs, also on
// LastActiveConfig), pkg/processor (Config.SettingRefs) and pkg/pipeline
// (DLQ.SettingRefs). Only the pipeline config parser in its keep-references
// mode may populate them; nothing reachable from the API does.
//
// There is deliberately no provider interface: the environment is the only
// source in v0.21. External secret stores (v0.22) extend the grammar and add
// cases behind Resolve with their own design doc.
package secretref
