// Copyright © 2024 Meroxa, Inc.
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

package avro

import (
	"context"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"testing"
)

func TestConfig_Parse(t *testing.T) {
	testCases := []struct {
		name    string
		input   map[string]string
		want    encodeConfig
		wantErr error
	}{
		{
			name: "only required",
			input: map[string]string{
				"url":                          "http://localhost",
				"schema.strategy":              "preRegistered",
				"schema.preRegistered.subject": "testsubject",
				"schema.preRegistered.version": "123",
			},
			want: encodeConfig{
				URL:   "http://localhost",
				Field: ".Payload.After",
				Schema: schemaConfig{
					Strategy: "preRegistered",
					PreRegistered: preRegisteredConfig{
						Subject: "testsubject",
						Version: 123,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got, err := parseConfig(context.Background(), tc.input)
			is.True(areErrorsEqual(tc.wantErr, err))
			diff := cmp.Diff(tc.want, *got, cmpopts.IgnoreUnexported(encodeConfig{}))
			if diff != "" {
				t.Errorf("mismatch (-want +got): %s", diff)
			}
		})
	}
}

func areErrorsEqual(e1, e2 error) bool {
	switch {
	case e1 == nil && e2 == nil:
		return true
	case e1 != nil && e2 != nil:
		return e1.Error() == e2.Error()
	default:
		return false
	}
}
