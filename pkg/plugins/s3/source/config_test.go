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

package source

import (
	"testing"
	"time"
)

var exampleConfig = map[string]string{
	"aws.access-key-id":     "access-key-123",
	"aws.secret-access-key": "secret-key-321",
	"aws.region":            "us-west-2",
	"aws.bucket":            "foobucket",
}

func configWith(pairs ...string) map[string]string {
	cfg := make(map[string]string)

	for key, value := range exampleConfig {
		cfg[key] = value
	}

	for i := 0; i < len(pairs); i += 2 {
		key := pairs[i]
		value := pairs[i+1]
		cfg[key] = value
	}

	return cfg
}

func TestPollingPeriod(t *testing.T) {
	c, err := Parse(configWith("polling-period", "5s"))

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if c.PollingPeriod != 5*time.Second {
		t.Fatalf("expected Polling Period to be %q, got %q", "5s", c.PollingPeriod)
	}
}
