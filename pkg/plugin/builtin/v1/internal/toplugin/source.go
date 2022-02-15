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

package toplugin

import (
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/connector-plugin/cpluginv1"
)

func SourceConfigureRequest(in map[string]string) (cpluginv1.SourceConfigureRequest, error) {
	out := cpluginv1.SourceConfigureRequest{}
	if len(in) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = in
	}
	return out, nil
}

func SourceStartRequest(in record.Position) (cpluginv1.SourceStartRequest, error) {
	out := cpluginv1.SourceStartRequest{
		Position: in,
	}
	return out, nil
}

func SourceRunRequest(in record.Position) (cpluginv1.SourceRunRequest, error) {
	out := cpluginv1.SourceRunRequest{
		AckPosition: in,
	}
	return out, nil
}

func SourceStopRequest() cpluginv1.SourceStopRequest {
	return cpluginv1.SourceStopRequest{}
}

func SourceTeardownRequest() cpluginv1.SourceTeardownRequest {
	return cpluginv1.SourceTeardownRequest{}
}
