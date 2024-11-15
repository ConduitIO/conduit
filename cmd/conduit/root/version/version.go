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

package version

import (
	"context"
	"fmt"

	"github.com/conduitio/ecdysis"
)

type VersionCommand struct{}

var (
	_ ecdysis.CommandWithExecute = (*VersionCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*VersionCommand)(nil)
)

func (c *VersionCommand) Usage() string { return "version" }
func (c *VersionCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Print the version number of conduit",
	}
}

func (c *VersionCommand) Execute(context.Context) error {
	fmt.Println("conduit v0.1.0")
	return nil
}
