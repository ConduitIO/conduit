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

package processors

import (
	"testing"

	"github.com/conduitio/conduit/pkg/scaffold"
	"github.com/stretchr/testify/assert"
)

func TestNewNewCommand(t *testing.T) {
	c := newNewCommand()
	assert.Equal(t, scaffold.KindProcessor, c.Kind)
	assert.Equal(t, "processor.new", c.ResultCommand())
	assert.Equal(t, "new [name]", c.Usage())
	assert.NotEmpty(t, c.Docs().Short)
	assert.Contains(t, c.Docs().Example, "conduit processor new")
}

func TestProcessorsCommand_IncludesNew(t *testing.T) {
	subs := (&ProcessorsCommand{}).SubCommands()
	var found bool
	for _, s := range subs {
		if s.Usage() == "new [name]" {
			found = true
		}
	}
	assert.True(t, found, "processors SubCommands() should register new")
}
