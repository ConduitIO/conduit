package internal

import (
	"fmt"
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/spf13/viper"
)

const (
	CONDUIT_PREFIX = "CONDUIT"
)

// LoadConfigFromFile loads on cfg, the configuration from the file at path.
func LoadConfigFromFile(filePath string, cfg *conduit.Config) error {
	v := viper.New()

	// Set the file name and path
	v.SetConfigFile(filePath)

	// Attempt to read the configuration file
	if err := v.ReadInConfig(); err != nil {
		// here we could simply log conduit.yaml file doesn't exist since this is optional
		//return fmt.Errorf("error reading config file: %w", err)
		return nil
	}

	// Unmarshal the config into the cfg struct
	if err := v.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("unable to decode into struct: %w", err)
	}

	return nil
}

// TODO: check if logger is correct
func LoadConfigFromEnv(cfg *conduit.Config) error {
	v := viper.New()

	// Set environment variable prefix
	v.SetEnvPrefix(CONDUIT_PREFIX)

	// Automatically map environment variables
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		key := pair[0]
		value := pair[1]

		// Check if the environment variable has the desired prefix
		if strings.HasPrefix(key, fmt.Sprintf("%s_", CONDUIT_PREFIX)) {
			// Strip the prefix and replace underscores with dots
			strippedKey := strings.ToLower(strings.TrimPrefix(key, fmt.Sprintf("%s_", CONDUIT_PREFIX)))
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
