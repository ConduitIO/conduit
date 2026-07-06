// Copyright © 2026 Meroxa, Inc.
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

package quickstart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/matryer/is"
)

func TestQuickstartCommand_setup(t *testing.T) {
	is := is.New(t)
	c := &QuickstartCommand{}

	dir, cfg, err := c.setup()
	is.NoErr(err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Ephemeral workspace scaffolded: all three dirs + the demo pipeline file.
	for _, sub := range []string{"pipelines", "connectors", "processors"} {
		info, statErr := os.Stat(filepath.Join(dir, sub))
		is.NoErr(statErr)
		is.True(info.IsDir())
	}
	content, err := os.ReadFile(filepath.Join(dir, "pipelines", "quickstart.yaml"))
	is.NoErr(err)
	is.True(strings.Contains(string(content), "builtin:generator")) // source
	is.True(strings.Contains(string(content), "builtin:log"))       // destination

	// Config: in-memory store, points at the temp pipelines dir, API on, human logs.
	is.Equal(cfg.DB.Type, conduit.DBTypeInMemory)
	is.Equal(cfg.Pipelines.Path, filepath.Join(dir, "pipelines"))
	is.Equal(cfg.Connectors.Path, filepath.Join(dir, "connectors"))
	is.Equal(cfg.Processors.Path, filepath.Join(dir, "processors"))
	is.True(cfg.API.Enabled)
	is.True(cfg.Log.Format != "json") // default human-readable
}

func TestQuickstartCommand_setup_JSON(t *testing.T) {
	is := is.New(t)
	c := &QuickstartCommand{flags: QuickstartFlags{JSON: true}}

	dir, cfg, err := c.setup()
	is.NoErr(err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	is.Equal(cfg.Log.Format, logFormatJSON)
}
