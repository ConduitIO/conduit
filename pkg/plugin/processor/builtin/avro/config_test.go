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
			name: "preRegistered",
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
		{
			name: "autoRegister",
			input: map[string]string{
				"url":                         "http://localhost",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
			},
			want: encodeConfig{
				URL:   "http://localhost",
				Field: ".Payload.After",
				Schema: schemaConfig{
					Strategy:              "autoRegister",
					AutoRegisteredSubject: "testsubject",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got, err := parseConfig(context.Background(), tc.input)
			areErrorsEqual(is, tc.wantErr, err)
			diff := cmp.Diff(tc.want, *got, cmpopts.IgnoreUnexported(encodeConfig{}))
			if diff != "" {
				t.Errorf("mismatch (-want +got): %s", diff)
			}
		})
	}
}

func areErrorsEqual(is *is.I, want, got error) {
	if want != nil {
		is.True(got != nil) // expected an error
		is.Equal(want.Error(), got.Error())
	} else {
		is.NoErr(got)
	}
}
