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

// paddedBatchKey is "sdk.batch.size" with a leading space. The predicate trims
// keys before comparing, so a padded key is the same setting; the leading space
// also makes it sort before the plain key, which the determinism test relies on.
const paddedBatchKey = " sdk.batch.size"

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
		{"padded key", map[string]string{paddedBatchKey: "3"}, paddedBatchKey, "3", true},
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
			"sdk.batch.size": "2",
			paddedBatchKey:   "3",
		})
		is.True(ok)
		is.Equal(key, paddedBatchKey) // leading space sorts before 's'
		is.Equal(value, "3")
	}
}

// TestSDKBatchSettings pins the parser the funnel uses to enable destination
// batching: it mirrors the SDK's predicate (size > 1 or delay > 0) and leaves
// unparsable values to the plugin's own validation.
func TestSDKBatchSettings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		settings    map[string]string
		wantSize    int
		wantDelay   string
		wantEnabled bool
	}{
		{"unset", map[string]string{}, 0, "0s", false},
		{"size only", map[string]string{"sdk.batch.size": "100"}, 100, "0s", true},
		{"delay only", map[string]string{"sdk.batch.delay": "10ms"}, 0, "10ms", true},
		{"both", map[string]string{"sdk.batch.size": "100", "sdk.batch.delay": "10ms"}, 100, "10ms", true},
		{"size one", map[string]string{"sdk.batch.size": "1"}, 1, "0s", false},
		{"zero delay", map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"}, 0, "0s", false},
		//nolint:gocritic // the padded keys/values are the point: trimming is under test
		{"padded", map[string]string{" sdk.batch.size ": " 100 ", "sdk.batch.delay": " 5ms "}, 100, "5ms", true},
		{"unparsable size ignored", map[string]string{"sdk.batch.size": "lots"}, 0, "0s", false},
		{"unparsable delay ignored", map[string]string{"sdk.batch.delay": "soon"}, 0, "0s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			size, delay, enabled := SDKBatchSettings(tc.settings)
			is.Equal(size, tc.wantSize)
			is.Equal(delay.String(), tc.wantDelay)
			is.Equal(enabled, tc.wantEnabled)
		})
	}
}

// TestSDKBatchDelayAboveZero mirrors the SDK's reading of sdk.batch.delay
// (#3321): trimmed key and value, time.ParseDuration, above 0 only.
func TestSDKBatchDelayAboveZero(t *testing.T) {
	for name, tc := range map[string]struct {
		settings map[string]string
		wantKey  string
		want     bool
	}{
		"1s":         {settings: map[string]string{"sdk.batch.delay": "1s"}, wantKey: "sdk.batch.delay", want: true},
		"padded":     {settings: map[string]string{" sdk.batch.delay ": " 10ms "}, wantKey: " sdk.batch.delay ", want: true},
		"0":          {settings: map[string]string{"sdk.batch.delay": "0"}},
		"0s":         {settings: map[string]string{"sdk.batch.delay": "0s"}},
		"negative":   {settings: map[string]string{"sdk.batch.delay": "-1s"}},
		"unparsable": {settings: map[string]string{"sdk.batch.delay": "soon"}},
		"unset":      {settings: nil},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			key, _, ok := SDKBatchDelayAboveZero(tc.settings)
			is.Equal(ok, tc.want)
			is.Equal(key, tc.wantKey)
		})
	}
}

// TestSDKSettingValue: an empty value counts as absent, as the SDK applies the
// connector default to it; padded keys match once, deterministically.
func TestSDKSettingValue(t *testing.T) {
	is := is.New(t)
	v, ok := SDKSettingValue(map[string]string{"sdk.batch.size": " 5 "}, "sdk.batch.size")
	is.True(ok)
	is.Equal(v, "5")
	_, ok = SDKSettingValue(map[string]string{"sdk.batch.size": "  "}, "sdk.batch.size")
	is.True(!ok)
	_, ok = SDKSettingValue(nil, "sdk.batch.size")
	is.True(!ok)
	//nolint:gocritic // the padded key is the point
	v, ok = SDKSettingValue(map[string]string{" sdk.batch.size": "1", "sdk.batch.size": "100"}, "sdk.batch.size")
	is.True(ok)
	is.Equal(v, "1") // the sorted-first key, as settingValue
}

// TestSDKBatchNonLiteral pins the predicate behind
// pipeline.archv2_destination_batching_not_literal (the Start guard, the
// live-apply preflight, validate and doctor share it): any "$" in an
// sdk.batch.* value is something the settings resolver would expand or
// collapse, so the engine and the plugin could read different values.
func TestSDKBatchNonLiteral(t *testing.T) {
	for name, tc := range map[string]struct {
		settings map[string]string
		want     []string
	}{
		"braced reference":       {settings: map[string]string{"sdk.batch.size": "${S3_BATCH}"}, want: []string{"sdk.batch.size"}},
		"bare reference":         {settings: map[string]string{"sdk.batch.delay": "$D"}, want: []string{"sdk.batch.delay"}},
		"reference inside value": {settings: map[string]string{"sdk.batch.delay": "${N}ms"}, want: []string{"sdk.batch.delay"}},
		"escaped dollar":         {settings: map[string]string{"sdk.batch.size": "$$5"}, want: []string{"sdk.batch.size"}},
		"padded key":             {settings: map[string]string{paddedBatchKey: "${N}"}, want: []string{paddedBatchKey}},
		"both, sorted": {
			settings: map[string]string{"sdk.batch.size": "${N}", "sdk.batch.delay": "${D}"},
			want:     []string{"sdk.batch.delay", "sdk.batch.size"},
		},
		"literals":                {settings: map[string]string{"sdk.batch.size": "1000", "sdk.batch.delay": "1s"}},
		"other setting reference": {settings: map[string]string{"url": "${URL}", "sdk.batch.size": "10"}},
		"wrong-case key":          {settings: map[string]string{"SDK.BATCH.SIZE": "${N}"}},
		"unset":                   {settings: nil},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(SDKBatchNonLiteral(tc.settings), tc.want)
		})
	}
}
