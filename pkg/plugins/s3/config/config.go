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

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

const (
	// ConfigKeyAWSAccessKeyID is the config name for AWS access secret key
	ConfigKeyAWSAccessKeyID = "aws.access-key-id"

	// ConfigKeyAWSSecretAccessKey is the config name for AWS secret access key
	ConfigKeyAWSSecretAccessKey = "aws.secret-access-key" // nolint:gosec // false positive

	// ConfigKeyAWSRegion is the config name for AWS region
	ConfigKeyAWSRegion = "aws.region"

	// ConfigKeyAWSBucket is the config name for AWS S3 bucket
	ConfigKeyAWSBucket = "aws.bucket"
)

// Config represents configuration needed for S3
type Config struct {
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegion          string
	AWSBucket          string
}

// Parse attempts to parse plugins.Config into a Config struct
func Parse(cfg map[string]string) (Config, error) {
	accessKeyID, ok := cfg[ConfigKeyAWSAccessKeyID]

	if !ok {
		return Config{}, requiredConfigErr(ConfigKeyAWSAccessKeyID)
	}

	secretAccessKey, ok := cfg[ConfigKeyAWSSecretAccessKey]

	if !ok {
		return Config{}, requiredConfigErr(ConfigKeyAWSSecretAccessKey)
	}

	region, ok := cfg[ConfigKeyAWSRegion]

	if !ok {
		return Config{}, requiredConfigErr(ConfigKeyAWSRegion)
	}

	bucket, ok := cfg[ConfigKeyAWSBucket]

	if !ok {
		return Config{}, requiredConfigErr(ConfigKeyAWSBucket)
	}

	config := Config{
		AWSAccessKeyID:     accessKeyID,
		AWSSecretAccessKey: secretAccessKey,
		AWSRegion:          region,
		AWSBucket:          bucket,
	}

	return config, nil
}

func requiredConfigErr(name string) error {
	return cerrors.Errorf("%q config value must be set", name)
}
