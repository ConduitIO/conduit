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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
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
					StrategyType: "preRegistered",
					PreRegistered: preRegisteredConfig{
						Subject: "testsubject",
						Version: 123,
					},
				},
			},
		},
		{
			name: "preRegistered without version",
			input: map[string]string{
				"url":                          "http://localhost",
				"schema.strategy":              "preRegistered",
				"schema.preRegistered.subject": "testsubject",
			},
			wantErr: cerrors.New("failed parsing schema strategy: version needs to be positive: 0"),
		},
		{
			name: "preRegistered without subject",
			input: map[string]string{
				"url":                          "http://localhost",
				"schema.strategy":              "preRegistered",
				"schema.preRegistered.version": "123",
			},
			wantErr: cerrors.New("failed parsing schema strategy: subject required for schema strategy 'preRegistered'"),
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
					StrategyType:          "autoRegister",
					AutoRegisteredSubject: "testsubject",
				},
			},
		},
		{
			name: "autoRegister without subject",
			input: map[string]string{
				"url":             "http://localhost",
				"schema.strategy": "autoRegister",
			},
			wantErr: cerrors.New("failed parsing schema strategy: subject required for schema strategy 'autoRegister'"),
		},
		{
			name: "non-default target field",
			input: map[string]string{
				"url":                         "http://localhost",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
				"field":                       ".Payload.After.something",
			},
			want: encodeConfig{
				Field: ".Payload.After.something",
				URL:   "http://localhost",
				Schema: schemaConfig{
					StrategyType:          "autoRegister",
					AutoRegisteredSubject: "testsubject",
				},
			},
		},
		{
			name: "valid auth",
			input: map[string]string{
				"url":                         "http://localhost",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
				"auth.basic.username":         "user@example.com",
				"auth.basic.password":         "Passw0rd",
			},
			want: encodeConfig{
				URL:   "http://localhost",
				Field: ".Payload.After",
				Schema: schemaConfig{
					StrategyType:          "autoRegister",
					AutoRegisteredSubject: "testsubject",
				},
				Auth: authConfig{
					Username: "user@example.com",
					Password: "Passw0rd",
				},
			},
		},
		{
			name: "auth -- no username",
			input: map[string]string{
				"url":                         "http://localhost",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
				"auth.basic.password":         "Passw0rd",
			},
			wantErr: cerrors.New("invalid basic auth: specify a username to enable basic auth or remove field password"),
		},
		{
			name: "auth -- no password",
			input: map[string]string{
				"url":                         "http://localhost",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
				"auth.basic.username":         "username@example.com",
			},
			wantErr: cerrors.New("invalid basic auth: specify a password to enable basic auth or remove field username"),
		},
	}

	cmpOpts := cmpopts.IgnoreUnexported(encodeConfig{}, schemaConfig{}, tlsConfig{})
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got, gotErr := parseConfig(context.Background(), tc.input)
			if tc.wantErr != nil {
				is.True(gotErr != nil) // expected an error
				is.Equal(tc.wantErr.Error(), gotErr.Error())

				return
			}

			is.NoErr(gotErr)
			diff := cmp.Diff(tc.want, *got, cmpOpts)
			if diff != "" {
				t.Errorf("mismatch (-want +got): %s", diff)
			}
		})
	}
}
