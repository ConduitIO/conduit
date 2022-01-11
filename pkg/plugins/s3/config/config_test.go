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

package config

import (
	"testing"
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

func configWithout(keys ...string) map[string]string {
	cfg := make(map[string]string)

	for key, value := range exampleConfig {
		cfg[key] = value
	}

	for _, key := range keys {
		delete(cfg, key)
	}

	return cfg
}

func TestAWSAccessKeyID(t *testing.T) {
	t.Run("Successful", func(t *testing.T) {
		c, err := Parse(configWith("aws.access-key-id", "some-value"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.AWSAccessKeyID != "some-value" {
			t.Fatalf("expected AWSAccessKeyID to be %q, got %q", "some-value", c.AWSAccessKeyID)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := Parse(configWithout("aws.access-key-id"))

		if err == nil {
			t.Fatal("expected error, got nothing")
		}

		expectedErrMsg := `"aws.access-key-id" config value must be set`

		if err.Error() != expectedErrMsg {
			t.Fatalf("expected error msg to be %q, got %q", expectedErrMsg, err.Error())
		}
	})
}

func TestAWSSecretAccessKey(t *testing.T) {
	t.Run("Successful", func(t *testing.T) {
		c, err := Parse(configWith("aws.secret-access-key", "some-value"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.AWSSecretAccessKey != "some-value" {
			t.Fatalf("expected AWSSecretAccessKey to be %q, got %q", "some-value", c.AWSSecretAccessKey)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := Parse(configWithout("aws.secret-access-key"))

		if err == nil {
			t.Fatal("expected error, got nothing")
		}

		expectedErrMsg := `"aws.secret-access-key" config value must be set`

		if err.Error() != expectedErrMsg {
			t.Fatalf("expected error msg to be %q, got %q", expectedErrMsg, err.Error())
		}
	})
}

func TestAWSRegion(t *testing.T) {
	t.Run("Successful", func(t *testing.T) {
		c, err := Parse(configWith("aws.region", "us-west-2"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.AWSRegion != "us-west-2" {
			t.Fatalf("expected AWSRegion to be %q, got %q", "us-west-2", c.AWSRegion)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := Parse(configWithout("aws.region"))

		if err == nil {
			t.Fatal("expected error, got nothing")
		}

		expectedErrMsg := `"aws.region" config value must be set`

		if err.Error() != expectedErrMsg {
			t.Fatalf("expected error msg to be %q, got %q", expectedErrMsg, err.Error())
		}
	})
}

func TestAWSBucket(t *testing.T) {
	t.Run("Successful", func(t *testing.T) {
		c, err := Parse(configWith("aws.bucket", "foobar"))

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if c.AWSBucket != "foobar" {
			t.Fatalf("expected AWSBucket to be %q, got %q", "foobar", c.AWSBucket)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := Parse(configWithout("aws.bucket"))

		if err == nil {
			t.Fatal("expected error, got nothing")
		}

		expectedErrMsg := `"aws.bucket" config value must be set`

		if err.Error() != expectedErrMsg {
			t.Fatalf("expected error msg to be %q, got %q", expectedErrMsg, err.Error())
		}
	})
}
