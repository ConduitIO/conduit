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

package open

import (
	"context"

	"github.com/conduitio/ecdysis"
	"github.com/pkg/browser"
)

const ConduitDocsURL = "https://conduit.io/docs"

var (
	_ ecdysis.CommandWithDocs    = (*DocsCommand)(nil)
	_ ecdysis.CommandWithExecute = (*DocsCommand)(nil)
)

type DocsCommand struct{}

func (c *DocsCommand) Execute(_ context.Context) error {
	browser.OpenURL(ConduitDocsURL)
	return nil
}

func (c *DocsCommand) Usage() string { return "docs" }

func (c *DocsCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Open Conduit documentation in a web browser",
	}
}
