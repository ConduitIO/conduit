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

package root

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/viper"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
)

const ConduitPrefix = "CONDUIT"

type RootFlags struct {
	// Global flags -----------------------------------------------------------

	// Conduit configuration file
	ConduitConfigPath string `long:"config.path" usage:"global conduit configuration file" persistent:"true" default:"./conduit.yaml"`

	// Version
	Version bool `long:"version" short:"v" usage:"show current Conduit version" persistent:"true"`

	conduit.Config
}

type RootCommand struct {
	flags RootFlags
	cfg   conduit.Config
}

func (c *RootCommand) parseConfig(flagSet *flag.FlagSet) error {
	v := viper.New()

	// set configuration based on either the default or the provided value by the user.
	c.cfg = conduit.DefaultConfigWithBasePath(c.flags.ConduitCfgPath)

	configMap := map[string]interface{}{
		"api.enabled":                c.cfg.API.Enabled,
		"config.path":                c.cfg.ConduitCfgPath,
		"connectors.path":            c.cfg.Connectors.Path,
		"db.type":                    c.cfg.DB.Type,
		"dev.blockprofile":           c.cfg.Dev.BlockProfile,
		"dev.cpuprofile":             c.cfg.Dev.CPUProfile,
		"dev.memprofile":             c.cfg.Dev.MemProfile,
		"log.format":                 c.cfg.Log.Format,
		"log.level":                  c.cfg.Log.Level,
		"pipelines.exit-on-degraded": c.cfg.Pipelines.ExitOnDegraded,
		"pipelines.error-recovery.backoff-factor":     c.cfg.Pipelines.ErrorRecovery.BackoffFactor,
		"pipelines.error-recovery.max-delay":          c.cfg.Pipelines.ErrorRecovery.MaxDelay,
		"pipelines.error-recovery.max-retries":        c.cfg.Pipelines.ErrorRecovery.MaxRetries,
		"pipelines.error-recovery.max-retries-window": c.cfg.Pipelines.ErrorRecovery.MaxRetriesWindow,
		"pipelines.error-recovery.min-delay":          c.cfg.Pipelines.ErrorRecovery.MinDelay,
		"pipelines.path":                              c.cfg.Pipelines.Path,
		"preview.pipeline-arch-v2":                    c.cfg.Preview.PipelineArchV2,
		"processors.path":                             c.cfg.Processors.Path,
		"schema-registry.confluent.connection-string": c.cfg.SchemaRegistry.Confluent.ConnectionString,
		"schema-registry.type":                        c.cfg.SchemaRegistry.Type,
	}

	for key, value := range configMap {
		v.SetDefault(key, value)
	}

	// Read configuration from file
	v.SetConfigFile(c.flags.ConduitConfigPath)

	// ignore if file doesn't exist. Maybe we could check if user is trying read from a file that doesn't exist.
	_ = v.ReadInConfig()

	// Set environment variable prefix and automatic mapping
	v.SetEnvPrefix(ConduitPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	for key := range configMap {
		if err := v.BindEnv(key); err != nil {
			return cerrors.Errorf("error binding environment variable for key %q: %v\n", key, err)
		}
	}

	if err := v.Unmarshal(&c.cfg); err != nil {
		return fmt.Errorf("unable to unmarshal the configuration: %w", err)
	}

	return nil
}

func (c *RootCommand) Execute(_ context.Context) error {
	if c.flags.Version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		return nil
	}

	flagSet := flag.NewFlagSet("conduit", flag.ContinueOnError)
	if err := c.parseConfig(flagSet); err != nil {
		return err
	}

	e := &conduit.Entrypoint{}
	e.Serve(c.cfg)
	return nil
}

func (c *RootCommand) Usage() string { return "conduit" }
func (c *RootCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	defaultCfg := conduit.DefaultConfig()
	flags.SetDefault("config.path", defaultCfg.ConduitCfgPath)
	return flags
}

func (c *RootCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Conduit CLI",
		Long:  `Conduit CLI is a command-line that helps you interact with and manage Conduit.`,
	}
}

func (c *RootCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{
		&InitCommand{cfg: &c.cfg, rootFlags: &c.flags},
		&pipelines.PipelinesCommand{},
	}
}
