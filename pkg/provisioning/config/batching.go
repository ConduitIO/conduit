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
// reads to buffer writes. Arch-v2's stop-and-wait destination task cannot run
// it above 1 (CodeArchV2DestinationBatchingUnsupported); the engine guard in
// pkg/lifecycle-poc and the offline validate finding share this predicate so
// the two report the same refusal.
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
// reads to flush a partial batch by time. Like SDKBatchSizeSetting, it is read
// by the arch-v2 engine so the funnel can batch writes itself, and it must be
// stripped from the settings handed to the plugin so the SDK does not also
// batch (see pkg/lifecycle-poc and docs/design-documents/
// 20261017-funnel-destination-batching.md).
const SDKBatchDelaySetting = "sdk.batch.delay"

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
	var keys []string
	for k := range settings {
		if strings.TrimSpace(k) == target {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		return strings.TrimSpace(settings[k])
	}
	return ""
}
