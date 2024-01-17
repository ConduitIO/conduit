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
	"context"
	"testing"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/wasm"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestWASMProcessor_SpecifyError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.New(zerolog.NewTestWriter(t)))

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, SpecifyError)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	_, err = underTest.Specification()
	is.Equal(err, wasm.NewError(0, "boom"))
}

func TestWASMProcessor_Specify(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.New(zerolog.NewTestWriter(t)))

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, SimpleProcessor)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	gotSpec, err := underTest.Specification()
	is.NoErr(err)

	is.Equal(
		gotSpec,
		sdk.Specification{
			Name:        "test-processor",
			Summary:     "test processor's summary",
			Description: "test processor's description",
			Version:     "v1.3.5",
			Author:      "Meroxa, Inc.",
			Parameters: map[string]sdk.Parameter{
				"path": {
					Default:     "/",
					Type:        sdk.ParameterTypeString,
					Description: "path to something",
					Validations: []sdk.Validation{
						{
							Type:  sdk.ValidationTypeRegex,
							Value: "abc.*",
						},
					},
				},
			},
		},
	)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.New(zerolog.NewTestWriter(t)))

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, SimpleProcessor)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, nil)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}
