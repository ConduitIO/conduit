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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/s3/config"
)

const (
	// ConfigKeyPollingPeriod is the config name for the S3 CDC polling period
	ConfigKeyPollingPeriod = "polling-period"

	// DefaultPollingPeriod is the value assumed for the pooling period when the
	// config omits the polling period parameter
	DefaultPollingPeriod = "1s"
)

// Config represents source configuration with S3 configurations
type Config struct {
	config.Config
	PollingPeriod time.Duration
}

// Parse attempts to parse plugins.Config into a Config struct that Source could
// utilize
func Parse(cfg map[string]string) (Config, error) {
	common, err := config.Parse(cfg)
	if err != nil {
		return Config{}, err
	}

	pollingPeriodString, exists := cfg[ConfigKeyPollingPeriod]
	if !exists || pollingPeriodString == "" {
		pollingPeriodString = DefaultPollingPeriod
	}
	pollingPeriod, err := time.ParseDuration(pollingPeriodString)
	if err != nil {
		return Config{}, cerrors.Errorf(
			"%q config value should be a valid duration",
			ConfigKeyPollingPeriod,
		)
	}
	if pollingPeriod <= 0 {
		return Config{}, cerrors.Errorf(
			"%q config value should be positive, got %s",
			ConfigKeyPollingPeriod,
			pollingPeriod,
		)
	}

	sourceConfig := Config{
		Config:        common,
		PollingPeriod: pollingPeriod,
	}

	return sourceConfig, nil
}
