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
	"testing"

	"github.com/matryer/is"
)

// TestSDKBatchSizeAboveOne pins the predicate the engine guard and the offline
// validate finding share: it mirrors how the SDK reads sdk.batch.size (key and
// value trimmed; value parsed with Atoi; above 1 refused), and leaves an
// unparsable value to the plugin's own validation.
func TestSDKBatchSizeAboveOne(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		wantKey  string
		wantVal  string
		wantOK   bool
	}{
		{"above one", map[string]string{"sdk.batch.size": "5"}, "sdk.batch.size", "5", true},
		{"two", map[string]string{"sdk.batch.size": "2"}, "sdk.batch.size", "2", true},
		{"one", map[string]string{"sdk.batch.size": "1"}, "", "", false},
		{"zero", map[string]string{"sdk.batch.size": "0"}, "", "", false},
		{"negative", map[string]string{"sdk.batch.size": "-3"}, "", "", false},
		{"empty", map[string]string{"sdk.batch.size": ""}, "", "", false},
		{"unset", map[string]string{}, "", "", false},
		{"unparsable", map[string]string{"sdk.batch.size": "lots"}, "", "", false},
		{"padded key", map[string]string{" sdk.batch.size": "3"}, " sdk.batch.size", "3", true},
		{"padded value", map[string]string{"sdk.batch.size": " 4 "}, "sdk.batch.size", "4", true},
		{"unrelated setting", map[string]string{"sdk.batch.delay": "9"}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			key, value, ok := SDKBatchSizeAboveOne(tc.settings)
			is.Equal(ok, tc.wantOK)
			is.Equal(key, tc.wantKey)
			is.Equal(value, tc.wantVal)
		})
	}
}

// TestSDKBatchSizeAboveOne_PaddedAndPlainIsDeterministic pins the sort: when a
// map carries both a padded and a plain key, the padded key (leading space
// sorts first) wins deterministically rather than depending on map order.
func TestSDKBatchSizeAboveOne_PaddedAndPlainIsDeterministic(t *testing.T) {
	is := is.New(t)
	for i := 0; i < 20; i++ {
		key, value, ok := SDKBatchSizeAboveOne(map[string]string{
			"sdk.batch.size":  "2",
			" sdk.batch.size": "3",
		})
		is.True(ok)
		is.Equal(key, " sdk.batch.size") // leading space sorts before 's'
		is.Equal(value, "3")
	}
}
