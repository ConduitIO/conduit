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

package destination

import (
	"testing"

	"github.com/conduitio/conduit/pkg/plugins/s3/destination/format"
)

var exampleConfig = map[string]string{
	"aws.access-key-id":     "access-key-123",
	"aws.secret-access-key": "secret-key-321",
	"aws.region":            "us-west-2",
	"aws.bucket":            "foobucket",
	"format":                "json",
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

func TestFormat(t *testing.T) {
	t.Run("parquet", func(t *testing.T) {
		c, err := Parse(configWith("format", "parquet"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.Format != format.Parquet {
			t.Fatalf("expected Format to be %s, got %s", format.Parquet, c.Format)
		}
	})

	t.Run("json", func(t *testing.T) {
		c, err := Parse(configWith("format", "json"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.Format != format.JSON {
			t.Fatalf("expected Format to be %s, got %s", format.JSON, c.Format)
		}
	})

	t.Run("Invalid", func(t *testing.T) {
		_, err := Parse(configWith("format", "invalid"))

		if err == nil {
			t.Fatal("expected error, got nothing")
		}

		expectedErrMsg := `"format" config value should be one of (parquet, json)`

		if err.Error() != expectedErrMsg {
			t.Fatalf("expected error msg to be %q, got %q", expectedErrMsg, err.Error())
		}
	})
}

func TestPrefix(t *testing.T) {
	c, err := Parse(configWith("prefix", "some/value"))

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if c.Prefix != "some/value" {
		t.Fatalf("expected Prefix to be %q, got %q", "some/value", c.Prefix)
	}
}
