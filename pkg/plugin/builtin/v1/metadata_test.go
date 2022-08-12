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

package builtinv1

import (
	"testing"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/record"
)

func TestMetadataConstants(t *testing.T) {
	wantMapping := map[string]string{
		record.OpenCDCVersion:               cpluginv1.OpenCDCVersion,
		record.MetadataOpenCDCVersion:       cpluginv1.MetadataOpenCDCVersion,
		record.MetadataCreatedAt:            cpluginv1.MetadataCreatedAt,
		record.MetadataReadAt:               cpluginv1.MetadataReadAt,
		record.MetadataConduitPluginName:    cpluginv1.MetadataConduitPluginName,
		record.MetadataConduitPluginVersion: cpluginv1.MetadataConduitPluginVersion,
	}
	for conduitConstant, cpluginv1Constant := range wantMapping {
		if conduitConstant != cpluginv1Constant {
			t.Fatalf("conduit constant %q doesn't match cpluginv1 constant %q", conduitConstant, cpluginv1Constant)
		}
	}
}
