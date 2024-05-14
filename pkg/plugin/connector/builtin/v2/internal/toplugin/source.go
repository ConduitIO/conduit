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
	"maps"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/cpluginv2"
)

func SourceConfigureRequest(in map[string]string) cpluginv2.SourceConfigureRequest {
	out := cpluginv2.SourceConfigureRequest{}
	if len(in) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = maps.Clone(in)
	}
	return out
}

func SourceStartRequest(in opencdc.Position) cpluginv2.SourceStartRequest {
	return cpluginv2.SourceStartRequest{
		Position: in,
	}
}

func SourceRunRequest(in opencdc.Position) cpluginv2.SourceRunRequest {
	return cpluginv2.SourceRunRequest{
		AckPosition: in,
	}
}

func SourceStopRequest() cpluginv2.SourceStopRequest {
	return cpluginv2.SourceStopRequest{}
}

func SourceTeardownRequest() cpluginv2.SourceTeardownRequest {
	return cpluginv2.SourceTeardownRequest{}
}

func SourceLifecycleOnCreatedRequest(cfg map[string]string) cpluginv2.SourceLifecycleOnCreatedRequest {
	out := cpluginv2.SourceLifecycleOnCreatedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}

func SourceLifecycleOnUpdatedRequest(cfgBefore, cfgAfter map[string]string) cpluginv2.SourceLifecycleOnUpdatedRequest {
	out := cpluginv2.SourceLifecycleOnUpdatedRequest{}
	// gRPC sends `nil` if the map is empty, match behavior
	if len(cfgBefore) > 0 {
		out.ConfigBefore = cfgBefore
	}
	if len(cfgAfter) > 0 {
		out.ConfigAfter = cfgAfter
	}
	return out
}

func SourceLifecycleOnDeletedRequest(cfg map[string]string) cpluginv2.SourceLifecycleOnDeletedRequest {
	out := cpluginv2.SourceLifecycleOnDeletedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}
