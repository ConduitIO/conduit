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

package registry_test

import (
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry"
)

// TestNormalizeVersion_LeadingVTolerance is the explicit test plan-v2 §15.1
// requires: a "v0.14.0" request must normalize identically to a "0.14.0"
// one, on either side of the comparison.
func TestNormalizeVersion_LeadingVTolerance(t *testing.T) {
	is := is.New(t)

	withV, err := registry.NormalizeVersion("v0.14.0")
	is.NoErr(err)
	withoutV, err := registry.NormalizeVersion("0.14.0")
	is.NoErr(err)

	is.True(withV.Equal(withoutV))
	is.Equal(withV.String(), withoutV.String())
}

func TestNormalizeVersion_InvalidVersion(t *testing.T) {
	is := is.New(t)
	_, err := registry.NormalizeVersion("not-a-version")
	is.True(err != nil)
}

func TestNormalizeVersion_Comparison(t *testing.T) {
	is := is.New(t)

	older, err := registry.NormalizeVersion("v0.13.0")
	is.NoErr(err)
	newer, err := registry.NormalizeVersion("0.14.1")
	is.NoErr(err)

	is.True(older.LessThan(newer))
	is.True(older.Compare(newer) < 0)
}
