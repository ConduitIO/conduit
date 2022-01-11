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
	"fmt"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/s3/config"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/format"
)

const (
	// ConfigKeyBufferSize is the config name for buffer size.
	ConfigKeyBufferSize = "buffer-size"

	// ConfigKeyFormat is the config name for destination format.
	ConfigKeyFormat = "format"

	// ConfigKeyPrefix is the config name for S3 destination key prefix.
	ConfigKeyPrefix = "prefix"

	// MaxBufferSize determines maximum buffer size a config can accept.
	// When config with bigger buffer size is parsed, an error is returned.
	MaxBufferSize uint64 = 100000

	// DefaultBufferSize is the value BufferSize assumes when the config omits
	// the buffer size parameter
	DefaultBufferSize uint64 = 1000
)

// Config represents S3 configuration with Destination specific configurations
type Config struct {
	config.Config
	BufferSize uint64
	Format     format.Format
	Prefix     string
}

// Parse attempts to parse plugins.Config into a Config struct that Destination could
// utilize
func Parse(cfg map[string]string) (Config, error) {
	// first parse common fields
	common, err := config.Parse(cfg)
	if err != nil {
		return Config{}, err
	}

	bufferSizeString, exists := cfg[ConfigKeyBufferSize]
	if !exists || bufferSizeString == "" {
		bufferSizeString = fmt.Sprintf("%d", DefaultBufferSize)
	}

	bufferSize, err := strconv.ParseUint(bufferSizeString, 10, 32)

	if err != nil {
		return Config{}, cerrors.Errorf(
			"%q config value should be a positive integer",
			ConfigKeyBufferSize,
		)
	}

	if bufferSize > MaxBufferSize {
		return Config{}, cerrors.Errorf(
			"%q config value should not be bigger than %d, got %d",
			ConfigKeyBufferSize,
			MaxBufferSize,
			bufferSize,
		)
	}

	formatString, ok := cfg[ConfigKeyFormat]

	if !ok {
		return Config{}, requiredConfigErr(ConfigKeyFormat)
	}

	formatValue, err := format.Parse(formatString)

	if err != nil {
		var allFormats []string

		for _, f := range format.All {
			allFormats = append(allFormats, string(f))
		}

		return Config{}, cerrors.Errorf(
			"%q config value should be one of (%s)",
			ConfigKeyFormat,
			strings.Join(allFormats, ", "),
		)
	}

	prefix := cfg[ConfigKeyPrefix]

	destinationConfig := Config{
		Config:     common,
		BufferSize: bufferSize,
		Prefix:     prefix,
		Format:     formatValue,
	}

	return destinationConfig, nil
}

func requiredConfigErr(name string) error {
	return cerrors.Errorf("%q config value must be set", name)
}
