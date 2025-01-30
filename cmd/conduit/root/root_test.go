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
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/pflag"
)

func TestRootCommandFlags(t *testing.T) {
	is := is.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		usage      string
		persistent bool
	}{
		{longName: "version", shortName: "v", usage: "show the current Conduit version"},
		{longName: "api.grpc.address", usage: "address where Conduit is running", persistent: true},
		{longName: "config.path", usage: "path to the configuration file", persistent: true},
	}

	e := ecdysis.New()
	c := e.MustBuildCobraCommand(&RootCommand{})

	persistentFlags := c.PersistentFlags()
	cmdFlags := c.Flags()

	for _, f := range expectedFlags {
		var cf *pflag.Flag

		if f.persistent {
			cf = persistentFlags.Lookup(f.longName)
		} else {
			cf = cmdFlags.Lookup(f.longName)
		}
		is.True(cf != nil)
		is.Equal(f.longName, cf.Name)
		is.Equal(f.shortName, cf.Shorthand)
		is.Equal(cf.Usage, f.usage)
	}
}

func TestRootCommandExecuteWithVersionFlag(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	cmd := &RootCommand{
		flags: RootFlags{
			Version: true,
		},
	}
	cmd.Output(out)

	expectedOutput := strings.TrimSpace(conduit.Version(true))

	err := cmd.Execute(context.Background())
	is.NoErr(err)

	actualOutput := strings.TrimSpace(buf.String())

	is.Equal(actualOutput, expectedOutput)
}
