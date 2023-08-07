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

package mock

import (
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"go.uber.org/mock/gomock"
)

// CreateWithInstance is a utility function that lets you declare an expected call to
// Create with arguments taken from the supplied instance.
func (mr *PipelineServiceMockRecorder) CreateWithInstance(ctx interface{}, instance *pipeline.Instance) *gomock.Call {
	return mr.Create(ctx, instance.ID, instance.Config, instance.ProvisionedBy)
}

// CreateWithInstance is a utility function that lets you declare an expected call to
// Create with arguments taken from the supplied instance.
func (mr *ConnectorServiceMockRecorder) CreateWithInstance(ctx interface{}, instance *connector.Instance) *gomock.Call {
	return mr.Create(ctx, instance.ID, instance.Type, instance.Plugin, instance.PipelineID, instance.Config, instance.ProvisionedBy)
}

// CreateWithInstance is a utility function that lets you declare an expected call to
// Create with arguments taken from the supplied instance.
func (mr *ProcessorServiceMockRecorder) CreateWithInstance(ctx interface{}, instance *processor.Instance) *gomock.Call {
	return mr.Create(ctx, instance.ID, instance.Type, instance.Parent, instance.Config, instance.ProvisionedBy)
}
