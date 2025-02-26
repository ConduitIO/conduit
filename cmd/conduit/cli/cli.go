// Copyright © 2025 Meroxa, Inc.
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

package cli

import (
	"os"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
)

func Run(cfg conduit.Config) {
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithExecuteWithClientDecorator{}))

	cmd := e.MustBuildCobraCommand(&root.RootCommand{DefaultCfg: cfg})
	cmd.CompletionOptions.DisableDefaultCmd = true

	// Don't want to show usage when there's some unexpected error executing the command
	// Help will still be shown via --help
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err != nil {
		// error is already printed out
		os.Exit(1)
	}
	os.Exit(0)
}
