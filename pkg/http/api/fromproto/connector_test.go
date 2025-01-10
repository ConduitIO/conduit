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

package fromproto

import (
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
)

func TestConnectorConfig(t *testing.T) {
	testCases := []struct {
		name     string
		in       *apiv1.Connector_Config
		expected connector.Config
	}{
		{
			name:     "when input is nil",
			in:       nil,
			expected: connector.Config{},
		},
		{
			name: "when settings is nil",
			in: &apiv1.Connector_Config{
				Name: "test",
			},
			expected: connector.Config{
				Name:     "test",
				Settings: make(map[string]string),
			},
		},
		{
			name: "when settings is not nil",
			in: &apiv1.Connector_Config{
				Name:     "test",
				Settings: map[string]string{"key": "value"},
			},
			expected: connector.Config{
				Name:     "test",
				Settings: map[string]string{"key": "value"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			result := ConnectorConfig(tc.in)
			is.Equal(tc.expected, result)
		})
	}
}
