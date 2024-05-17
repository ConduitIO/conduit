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

package webhook

import (
	"testing"

	"github.com/matryer/is"
)

func TestHTTPConfig_ParseHeaders(t *testing.T) {
	testCases := []struct {
		name       string
		input      httpConfig
		wantConfig httpConfig
		wantErr    string
	}{
		{
			name: "ContentType field present, header present",
			input: httpConfig{
				ContentType: "application/json",
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
			},
			wantErr: `configuration error, cannot provide both "request.contentType" and "headers.Content-Type", use "headers.Content-Type" only`,
		},
		{
			name: "ContentType field present, header present, different case",
			input: httpConfig{
				ContentType: "application/json",
				Headers: map[string]string{
					"content-type": "application/json",
				},
			},
			wantErr: `configuration error, cannot provide both "request.contentType" and "headers.Content-Type", use "headers.Content-Type" only`,
		},
		{
			name: "ContentType field presents, header not present",
			input: httpConfig{
				ContentType: "application/json",
			},
			wantConfig: httpConfig{
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
			},
		},
		{
			name: "ContentType field not present, header present",
			input: httpConfig{
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
			},
			wantConfig: httpConfig{
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
			},
		},
		{
			name: "ContentType field not present, header present, different case",
			input: httpConfig{
				Headers: map[string]string{
					"content-type": "application/json",
				},
			},
			wantConfig: httpConfig{
				Headers: map[string]string{
					"content-type": "application/json",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := tc.input.parseHeaders()

			if tc.wantErr == "" {
				is.Equal(tc.wantConfig, tc.input)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			}
		})
	}
}
