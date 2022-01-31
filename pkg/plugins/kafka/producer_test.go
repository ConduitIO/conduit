// Copyright © 2022 Meroxa, Inc.
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

package kafka_test

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
)

func TestNewProducer_MissingRequired(t *testing.T) {
	testCases := []struct {
		name   string
		config kafka.Config
		exp    error
	}{
		{
			name:   "servers missing",
			config: kafka.Config{Topic: "topic"},
			exp:    kafka.ErrServersMissing,
		},
		{
			name:   "topic missing",
			config: kafka.Config{Servers: []string{"irrelevant servers"}},
			exp:    kafka.ErrTopicMissing,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			producer, err := kafka.NewProducer(tc.config)
			assert.Nil(t, producer)
			assert.Error(t, err)
			assert.True(t, cerrors.Is(err, tc.exp), "expected "+tc.exp.Error())
		})
	}
}
