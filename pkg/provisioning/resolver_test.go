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

package provisioning

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func TestResolveEnv(t *testing.T) {
	is := is.New(t)
	t.Setenv("SET_VAR", "value")
	t.Setenv("EMPTY_VAR", "")

	got, err := resolveEnv("prefix/${SET_VAR}/suffix")
	is.NoErr(err)
	is.Equal(got, "prefix/value/suffix")

	// an unset variable fails loudly with the stable code
	_, err = resolveEnv("${MISSING_VAR}")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeEnvVarMissing)

	// an empty variable resolves to the empty string, not an error
	got, err = resolveEnv("[${EMPTY_VAR}]")
	is.NoErr(err)
	is.Equal(got, "[]")
}

func TestResolveSettings(t *testing.T) {
	is := is.New(t)
	t.Setenv("AWS_SECRET", "s3cr3t")

	got, err := resolveSettings(map[string]string{
		"aws.secret": "${AWS_SECRET}",
		"plain":      "no-env",
	})
	is.NoErr(err)
	is.Equal(got, map[string]string{"aws.secret": "s3cr3t", "plain": "no-env"})

	// a missing variable fails and names the setting
	_, err = resolveSettings(map[string]string{"aws.key": "${MISSING}"})
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeEnvVarMissing)
}
