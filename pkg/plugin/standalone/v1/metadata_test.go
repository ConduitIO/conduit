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

package standalonev1

import (
	"testing"

	"github.com/conduitio/conduit/pkg/record"
	connectorv1 "go.buf.build/grpc/go/conduitio/conduit-connector-protocol/connector/v1"
	opencdcv1 "go.buf.build/grpc/go/conduitio/conduit-connector-protocol/opencdc/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/runtime/protoimpl"
)

func TestMetadataConstants(t *testing.T) {
	wantMapping := map[string]*protoimpl.ExtensionInfo{
		record.OpenCDCVersion:                          opencdcv1.E_OpencdcVersion,
		record.MetadataOpenCDCVersion:                  opencdcv1.E_MetadataVersion,
		record.MetadataCreatedAt:                       opencdcv1.E_MetadataCreatedAt,
		record.MetadataReadAt:                          opencdcv1.E_MetadataReadAt,
		record.MetadataConduitSourcePluginName:         connectorv1.E_MetadataConduitSourcePluginName,
		record.MetadataConduitSourcePluginVersion:      connectorv1.E_MetadataConduitSourcePluginVersion,
		record.MetadataConduitDestinationPluginName:    connectorv1.E_MetadataConduitDestinationPluginName,
		record.MetadataConduitDestinationPluginVersion: connectorv1.E_MetadataConduitDestinationPluginVersion,
	}
	for goConstant, extensionInfo := range wantMapping {
		protoConstant := proto.GetExtension(extensionInfo.TypeDescriptor().ParentFile().Options(), extensionInfo)
		if goConstant != protoConstant {
			t.Fatalf("go constant %q doesn't match proto constant %q", goConstant, protoConstant)
		}
	}
}
