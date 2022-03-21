// Copyright © 2022 Meroxa, Inc.
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

package orchestrator

import (
	"context"
	"reflect"
	"testing"

	"github.com/conduitio/conduit/pkg/orchestrator/mock"
	"github.com/golang/mock/gomock"
)

// ctxType can be used in tests in call to gomock.AssignableToTypeOf to assert
// a context is passed to a function.
var ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()

func newMockServices(t *testing.T) (*mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *mock.PluginService) {
	ctrl := gomock.NewController(t)

	return mock.NewPipelineService(ctrl),
		mock.NewConnectorService(ctrl),
		mock.NewProcessorService(ctrl),
		mock.NewPluginService(ctrl)
}
