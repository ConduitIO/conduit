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

package s3

import (
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/s3/config"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination"
	"github.com/conduitio/conduit/pkg/plugins/s3/source"
)

type Spec struct{}

// Specification returns the Plugin's Specification.
func Specification() sdk.Specification {
	return sdk.Specification{
		Name:    "s3",
		Summary: "An S3 source and destination plugin for Conduit, written in Go.",
		Version: "v0.0.1",
		Author:  "Meroxa, Inc.",
		DestinationParams: map[string]sdk.Parameter{
			config.ConfigKeyAWSAccessKeyID: {
				Default:     "",
				Required:    true,
				Description: "AWS access key id.",
			},
			config.ConfigKeyAWSSecretAccessKey: {
				Default:     "",
				Required:    true,
				Description: "AWS secret access key.",
			},
			config.ConfigKeyAWSRegion: {
				Default:     "",
				Required:    true,
				Description: "the AWS S3 bucket region.",
			},
			config.ConfigKeyAWSBucket: {
				Default:     "",
				Required:    true,
				Description: "the AWS S3 bucket name.",
			},
			destination.ConfigKeyBufferSize: {
				Default:     "1000",
				Required:    false,
				Description: `the buffer size {when full, the files will be written to destination}, max is "100000".`,
			},
			destination.ConfigKeyFormat: {
				Default:     "",
				Required:    false,
				Description: `the destination format, either "json" or "parquet".`,
			},
			destination.ConfigKeyPrefix: {
				Default:     "",
				Required:    false,
				Description: "the key prefix for S3 destination.",
			},
		},
		SourceParams: map[string]sdk.Parameter{
			config.ConfigKeyAWSAccessKeyID: {
				Default:     "",
				Required:    true,
				Description: "AWS access key id.",
			},
			config.ConfigKeyAWSSecretAccessKey: {
				Default:     "",
				Required:    true,
				Description: "AWS secret access key.",
			},
			config.ConfigKeyAWSRegion: {
				Default:     "",
				Required:    true,
				Description: "the AWS S3 bucket region.",
			},
			config.ConfigKeyAWSBucket: {
				Default:     "",
				Required:    true,
				Description: "the AWS S3 bucket name.",
			},
			source.ConfigKeyPollingPeriod: {
				Default:     source.DefaultPollingPeriod,
				Required:    false,
				Description: "polling period for the CDC mode, formatted as a time.Duration string.",
			},
		},
	}
}
