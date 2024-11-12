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

package builtin

import (
	"context"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"testing"

	"github.com/matryer/is"
)

func TestRegistry_InitList(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest := NewRegistry(log.Nop(), DefaultBuiltinConnectors, nil)

	underTest.Init(ctx)
	specs := underTest.List()

	is.Equal(len(DefaultBuiltinConnectors), len(specs))
	for _, gotSpec := range specs {
		wantSpec := DefaultBuiltinConnectors["github.com/conduitio/conduit-connector-"+gotSpec.Name].NewSpecification()
		is.Equal(
			"",
			cmp.Diff(
				pconnector.Specification(wantSpec),
				gotSpec,
				cmpopts.IgnoreFields(pconnector.Specification{}, "SourceParams", "DestinationParams"),
			),
		)
	}
}
