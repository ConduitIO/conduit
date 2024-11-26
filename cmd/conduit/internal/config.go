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

package internal

import (
	"fmt"
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/spf13/viper"
)

const (
	ConduitPrefix = "CONDUIT"
)

// LoadConfigFromFile loads on cfg, the configuration from the file at path.
func LoadConfigFromFile(filePath string, cfg *conduit.Config) error {
	v := viper.New()

	// Set the file name and path
	v.SetConfigFile(filePath)

	// Attempt to read the configuration file.
	if err := v.ReadInConfig(); err != nil {
		return nil
	}

	// Unmarshal the config into the cfg struct
	if err := v.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("unable to decode into struct: %w", err)
	}

	return nil
}

func LoadConfigFromEnv(cfg *conduit.Config) error {
	v := viper.New()

	// Set environment variable prefix
	v.SetEnvPrefix(ConduitPrefix)

	// Automatically map environment variables
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		key := pair[0]
		value := pair[1]

		// Check if the environment variable has the desired prefix
		if strings.HasPrefix(key, fmt.Sprintf("%s_", ConduitPrefix)) {
			// Strip the prefix and replace underscores with dots
			strippedKey := strings.ToLower(strings.TrimPrefix(key, fmt.Sprintf("%s_", ConduitPrefix)))
			strippedKey = strings.ReplaceAll(strippedKey, "_", ".")

			// Set the value in Viper
			v.Set(strippedKey, value)
		}
	}

	// Unmarshal the environment variables into the config struct
	err := v.Unmarshal(cfg)
	if err != nil {
		return fmt.Errorf("error unmarshalling config from environment variables: %w", err)
	}
	return nil
}
