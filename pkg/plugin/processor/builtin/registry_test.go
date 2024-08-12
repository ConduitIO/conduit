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

package builtin

import (
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	sdk "github.com/conduitio/conduit-processor-sdk"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestRegistry_List(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()

	ctrl := gomock.NewController(t)
	procPlugin := mock.NewProcessor(ctrl)

	procSpec := sdk.Specification{
		Name:    "test-processor",
		Version: "v0.1.2",
	}
	procPlugin.EXPECT().Specification().Return(procSpec, nil)
	procConstructor := func(log.CtxLogger) sdk.Processor { return procPlugin }

	wantList := map[plugin.FullName]sdk.Specification{
		"builtin:test-processor@v0.1.2": procSpec,
	}

	schemaRegistry, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)

	reg := NewRegistry(logger, map[string]ProcessorPluginConstructor{procSpec.Name: procConstructor}, schemaRegistry)

	got := reg.List()
	is.Equal(got, wantList)
}
