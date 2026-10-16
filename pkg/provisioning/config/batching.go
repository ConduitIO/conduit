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
