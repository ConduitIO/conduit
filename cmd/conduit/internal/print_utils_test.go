// Copyright © 2025 Meroxa, Inc.
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

package internal

import (
	"testing"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
)

func TestProcessorParentTypeToString(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name                string
		processorParentType apiv1.Processor_Parent_Type
		want                string
	}{
		{
			name:                "Connector",
			processorParentType: apiv1.Processor_Parent_TYPE_CONNECTOR,
			want:                "connector",
		},
		{
			name:                "Pipeline",
			processorParentType: apiv1.Processor_Parent_TYPE_PIPELINE,
			want:                "pipeline",
		},
		{
			name:                "Unspecified",
			processorParentType: apiv1.Processor_Parent_TYPE_UNSPECIFIED,
			want:                "unspecified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is.Equal(ProcessorParentToString(tt.processorParentType), tt.want)
		})
	}
}
