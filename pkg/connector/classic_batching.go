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
	"strconv"
	"strings"
)

// Classic-engine default destination batching. See
// docs/design-documents/20261020-classic-default-destination-batching.md.
//
// One record per Write is one round trip per record; on the classic engine the
// built-in Postgres and Kafka destinations measured 0.30x of Kafka Connect that
// way and 1.00x at 1000 records / 100ms (ConduitIO/conduit#3228).
const (
	// ClassicDefaultBatchSize is the sdk.batch.size the classic engine applies.
	ClassicDefaultBatchSize = "1000"
	// ClassicDefaultBatchDelay is the sdk.batch.delay the classic engine applies.
	ClassicDefaultBatchDelay = "100ms"

	sdkBatchSizeKey  = "sdk.batch.size"
	sdkBatchDelayKey = "sdk.batch.delay"

	// kafkaMirrorKey is the Kafka destination's mirror-mode setting
	// (conduit-connector-kafka destination.Config.Mirror, json:"mirror").
	kafkaMirrorKey = "mirror"

	// Names of the allowlisted built-in plugins (their connector spec names).
	builtinPostgres = "postgres"
	builtinKafka    = "kafka"
)

// classicBatchAllowlist is the set of built-in destination plugins that get
// the default. A plugin earns a place here with a benchi result and the
// acknowledgement tests; nothing else is batched on the user's behalf.
var classicBatchAllowlist = map[string]struct{}{
	builtinPostgres: {},
	builtinKafka:    {},
}

// classicBatchDefaults sets sdk.batch.size and sdk.batch.delay in settings (the
// resolved copy that is about to be handed to the plugin) to the classic
// defaults when all of these hold, and reports whether it did:
//
//   - builtin is the name of an allowlisted built-in plugin ("" means the
//     plugin is not built-in, for example a standalone plugin that shares the
//     bare name);
//   - the user set neither sdk.batch.size nor sdk.batch.delay. The two keys are
//     one unit: setting either one, to any value, means the user chose their
//     batching and nothing is injected. A key counts as set when its trimmed
//     value is non-empty, the rule the SDK's ApplyDefaults uses, and keys are
//     matched after trimming, as config.Sanitize does. Writing the default value
//     out ("1000") is therefore the same as leaving it unset;
//   - the Kafka destination is not in mirror mode. Mirror mode promises
//     per-partition order until the first produce error, and a batch widens the
//     window in which records after a failed one are already enqueued. A value
//     of mirror that is not a plain "false" also excludes it, so a mistyped
//     value is left to the connector's own validation, not batched.
//
// When it returns false, why names the reason ("" when the caller never asked,
// that is the plugin is not allowlisted, which is the common case and not worth
// a log line).
//
// Opt-out is "set sdk.batch.size to 1" (or 0): the key is set, so nothing is
// injected, the delay stays unset, and the SDK writes one record at a time on
// every SDK version.
func classicBatchDefaults(settings map[string]string, builtin string) (applied bool, why string) {
	if _, ok := classicBatchAllowlist[builtin]; !ok {
		return false, ""
	}
	if settingSet(settings, sdkBatchSizeKey) || settingSet(settings, sdkBatchDelayKey) {
		return false, "sdk.batch.size or sdk.batch.delay is set"
	}
	if builtin == builtinKafka && kafkaMirrorSelected(settings) {
		return false, "kafka mirror mode"
	}
	settings[sdkBatchSizeKey] = ClassicDefaultBatchSize
	settings[sdkBatchDelayKey] = ClassicDefaultBatchDelay
	return true, ""
}

// settingSet reports whether settings carries key (after trimming keys) with a
// non-empty trimmed value.
func settingSet(settings map[string]string, key string) bool {
	for k, v := range settings {
		if strings.TrimSpace(k) == key && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// kafkaMirrorSelected reports whether settings select (or might select) the
// Kafka destination's mirror mode: any non-empty mirror value other than one
// that parses as false.
func kafkaMirrorSelected(settings map[string]string) bool {
	for k, v := range settings {
		if strings.TrimSpace(k) != kafkaMirrorKey {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if b, err := strconv.ParseBool(v); err == nil && !b {
			continue
		}
		return true
	}
	return false
}
