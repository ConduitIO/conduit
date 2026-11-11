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

package config

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// SDKBatchSizeSetting is the destination/DLQ setting the conduit-connector-sdk
// reads to buffer writes. The arch-v2 engine batches a destination's writes
// itself with it, and refuses it above 1 on the dead-letter queue, which it
// does not batch (CodeArchV2DestinationBatchingUnsupported). The engine guard
// in pkg/lifecycle-poc and the offline validate finding share
// SDKBatchSizeAboveOne so the two report the same refusal.
const SDKBatchSizeSetting = "sdk.batch.size"

// SDKBatchSizeAboveOne reports whether settings carry an sdk.batch.size the SDK
// would read as larger than 1, returning the key as written and the trimmed
// value.
//
// It mirrors how conduit-connector-sdk reads the setting: config.Sanitize trims
// every key and value, so " sdk.batch.size" is the same setting (keys are
// otherwise case-sensitive). Validation then parses the value with
// strconv.Atoi, so the guard does the same. (Decoding then uses base 0, so
// "0144" is validated as 144 but decoded as octal 100; the octal reading is
// never larger than the decimal one, so Atoi alone decides.)
// A value Atoi rejects never reaches the batcher (the plugin fails its own
// config validation), so it is left to the plugin. Unset, empty, 0, 1 and
// negative values are not refused.
func SDKBatchSizeAboveOne(settings map[string]string) (key, value string, ok bool) {
	var keys []string
	for k := range settings {
		if strings.TrimSpace(k) == SDKBatchSizeSetting {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // deterministic when a map carries both padded and plain keys
	for _, k := range keys {
		v := strings.TrimSpace(settings[k])
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			return k, v, true
		}
	}
	return "", "", false
}

// SDKBatchDelaySetting is the destination/DLQ setting the conduit-connector-sdk
// reads to flush a partial batch by time. Like SDKBatchSizeSetting, the arch-v2
// engine batches a destination with it and hands the plugin "0" instead, so
// the SDK does not also batch (docs/design-documents/
// 20261103-archv2-destination-batch-timing.md, Decision 2), and refuses it
// above 0 on the dead-letter queue (SDKBatchDelayAboveZero).
const SDKBatchDelaySetting = "sdk.batch.delay"

// SDKBatchDelayAboveZero reports whether settings carry an sdk.batch.delay the
// SDK would read as larger than 0, returning the key as written and the
// trimmed value. It is SDKBatchSizeAboveOne's twin for the delay: the SDK
// batches a destination whose delay is above 0 even when its size is not set
// (it then uses a size of its own), so such a delay withholds acks the same
// way. The value is parsed with time.ParseDuration, as the SDK does; a value
// it rejects is left to the plugin's own validation.
func SDKBatchDelayAboveZero(settings map[string]string) (key, value string, ok bool) {
	var keys []string
	for k := range settings {
		if strings.TrimSpace(k) == SDKBatchDelaySetting {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // deterministic when a map carries both padded and plain keys
	for _, k := range keys {
		v := strings.TrimSpace(settings[k])
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return k, v, true
		}
	}
	return "", "", false
}

// SDKBatchSettings parses the destination/DLQ batching settings the connector
// SDK would read, mirroring its decoding: sdk.batch.size via strconv.Atoi and
// sdk.batch.delay via time.ParseDuration. It reports the effective (size, delay)
// and whether batching is enabled (size > 1 or delay > 0, exactly the SDK's
// configureWriteStrategy predicate). A value that does not parse is left to the
// plugin's own validation and contributes nothing here.
func SDKBatchSettings(settings map[string]string) (size int, delay time.Duration, enabled bool) {
	size, _ = strconv.Atoi(strings.TrimSpace(settingValue(settings, SDKBatchSizeSetting)))
	delay, _ = time.ParseDuration(strings.TrimSpace(settingValue(settings, SDKBatchDelaySetting)))
	return size, delay, size > 1 || delay > 0
}

// settingValue returns the trimmed value of the first setting key that matches
// target after trimming, mirroring how config.Sanitize trims keys. It is
// deterministic even when a map carries both padded and plain keys.
func settingValue(settings map[string]string, target string) string {
	v, _ := SDKSettingValue(settings, target)
	return v
}

// SDKSettingValue returns the trimmed value of the setting target as the
// connector SDK would read it: keys are matched after trimming (config.Sanitize
// trims every key and value), the first match in sorted key order wins so the
// result is deterministic when a map carries both padded and plain keys, and
// present is false when no key matches or the value is empty, because the SDK
// applies the connector's default to an empty value as it does to a missing
// key (config.ApplyDefaults).
func SDKSettingValue(settings map[string]string, target string) (value string, present bool) {
	var keys []string
	for k := range settings {
		if strings.TrimSpace(k) == target {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(settings[k])
		return v, v != ""
	}
	return "", false
}

// SDKBatchNonLiteral returns, sorted, every key of settings that names
// sdk.batch.size or sdk.batch.delay (after trimming, as the SDK matches keys)
// and whose value is not a literal, because it contains "$".
//
// The arch-v2 engine resolves a destination's batching from the stored
// settings, before ${VAR} references are expanded for the plugin (see
// pkg/foundation/env), and configures the plugin "0" only for the keys whose
// stored value turns its batcher on. A reference such as "${S3_BATCH}" parses
// as nothing for the engine but reaches the plugin expanded, so the plugin
// would batch behind the engine: double batching, or an ack withheld until a
// batch the engine never sends fills (a stall). The engine therefore refuses
// a non-literal value under arch-v2 (lifecycle.CodeArchV2DestinationBatchingNotLiteral).
//
// "$" is exactly what the settings resolver acts on: env.ResolveEnv returns a
// value without "$" unchanged, and expands or collapses ("$$") anything else.
// No valid integer or duration contains "$", so the refusal costs no
// literal configuration.
func SDKBatchNonLiteral(settings map[string]string) []string {
	var keys []string
	for k, v := range settings {
		switch strings.TrimSpace(k) {
		case SDKBatchSizeSetting, SDKBatchDelaySetting:
			if strings.Contains(v, "$") {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}
