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
	"errors"
	"testing"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestWASMProcessor_MalformedProcessor(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := NewWASMProcessor(ctx, zerolog.Nop(), testPluginDir+"malformed_processor/processor.txt")
	is.True(err != nil)
	is.Equal(err.Error(), "failed running WASM module: failed compiling WASM module: invalid magic number")
}

func TestWASMProcessor_SpecifyError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := NewWASMProcessor(ctx, zerolog.Nop(), testPluginDir+"specify_error/processor.wasm")
	is.NoErr(err)

	_, err = underTest.Specification()
	is.Equal(err, errors.New("boom"))
}

func TestWASMProcessor_Specify(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := NewWASMProcessor(
		ctx,
		zerolog.New(zerolog.NewTestWriter(t)),
		testPluginDir+"simple_processor/processor.wasm",
	)
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
