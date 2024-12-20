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

package version

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithExecute = (*VersionCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*VersionCommand)(nil)
)

type VersionCommand struct{}

func (c *VersionCommand) Usage() string { return "version" }

func (c *VersionCommand) Execute(_ context.Context) error {
	_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
	return nil
}

func (c *VersionCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Show the current version of Conduit.",
	}
}
