// Copyright © 2023 Meroxa, Inc.
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

package standalone

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestRegistry_List(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginChaosDir)
	is.NoErr(err)
	list := underTest.List()
	is.Equal(1, len(list))
	got, ok := list["standalone:chaos-processor@v1.3.5"]
	is.True(ok) // expected spec for standalone:chaos-processor@v1.3.5

	want := ChaosProcessorSpecifications()

	is.Equal(got, want)
}

func TestRegistry_MalformedProcessor(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginMalformedDir)
	is.NoErr(err)
	list := underTest.List()
	is.Equal(0, len(list))
}

func TestRegistry_SpecifyError(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginSpecifyErrorDir)
	is.NoErr(err)
	list := underTest.List()
	is.Equal(0, len(list))
}
