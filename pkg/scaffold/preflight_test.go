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

package scaffold

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflight_Passes(t *testing.T) {
	// go and git are both required to run this test suite at all, so a
	// clean environment should always pass preflight.
	_, err := preflight(context.Background())
	require.NoError(t, err)
}

func TestPreflight_MissingToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a directory with nothing on it

	_, err := preflight(context.Background())
	require.Error(t, err)

	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, CodeToolchainUnavailable.Reason(), ce.Code.Reason())
	assert.NotEmpty(t, ce.Suggestion)
	assert.Equal(t, exitcode.Environment, exitcode.ExitCode(err), "a preflight failure must classify to the environment exit bucket (3)")
}
