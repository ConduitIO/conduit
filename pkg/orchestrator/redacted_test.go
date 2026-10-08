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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestRestoreRedactedSettings(t *testing.T) {
	stored := map[string]string{"password": "s3cret", "host": "db", "token": "t0k"}

	testCases := []struct {
		name   string
		update map[string]string
		want   map[string]string
	}{
		{
			name:   "all redacted keeps every stored value",
			update: map[string]string{"password": log.Redacted, "host": log.Redacted, "token": log.Redacted},
			want:   stored,
		},
		{
			name:   "a real value replaces the stored one",
			update: map[string]string{"password": "new", "host": log.Redacted, "token": log.Redacted},
			want:   map[string]string{"password": "new", "host": "db", "token": "t0k"},
		},
		{
			name:   "a key left out stays out",
			update: map[string]string{"password": log.Redacted},
			want:   map[string]string{"password": "s3cret"},
		},
		{
			name:   "only an exact match is the placeholder",
			update: map[string]string{"password": " ***", "host": "****", "token": "a***"},
			want:   map[string]string{"password": " ***", "host": "****", "token": "a***"},
		},
		{
			name:   "a new key with a real value is added",
			update: map[string]string{"password": log.Redacted, "port": "5432"},
			want:   map[string]string{"password": "s3cret", "port": "5432"},
		},
		{
			name:   "empty update",
			update: map[string]string{},
			want:   map[string]string{},
		},
		{
			name:   "nil update stays nil",
			update: nil,
			want:   nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			before := map[string]string{}
			for k, v := range tc.update {
				before[k] = v
			}

			got, err := restoreRedactedSettings(stored, tc.update, "/config/settings")
			is.NoErr(err)
			is.Equal(got, tc.want)
			if tc.update != nil {
				is.Equal(tc.update, before) // the caller's map is not mutated
			}
		})
	}
}

func TestRestoreRedactedSettings_NoStoredValue(t *testing.T) {
	is := is.New(t)
	stored := map[string]string{"host": "db"}
	update := map[string]string{"host": log.Redacted, "z/key": log.Redacted, "a.key": log.Redacted}

	got, err := restoreRedactedSettings(stored, update, "/dlq/settings")
	is.True(got == nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeRedactedSettingWithoutStoredValue)
	is.True(strings.Contains(ce.Message, `"a.key", "z/key"`)) // every key, sorted
	is.Equal(ce.ConfigPath, "/dlq/settings/a.key")            // the first one
	is.True(ce.Suggestion != "")

	// RFC 6901 escaping of the pointer token.
	_, err = restoreRedactedSettings(nil, map[string]string{"a/b~c": log.Redacted}, "/config/settings")
	ce, _ = conduiterr.Get(err)
	is.Equal(ce.ConfigPath, "/config/settings/a~1b~0c")
}
