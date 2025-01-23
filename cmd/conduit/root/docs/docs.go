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

package docs

import (
	"github.com/conduitio/conduit/cmd/conduit/root/open"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithHidden = (*DocsCommand)(nil)
)

type DocsCommand struct {
	open.DocsCommand
}

func (d *DocsCommand) Hidden() bool {
	return true
}
