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

func DestinationConfigureRequest(in map[string]string) cpluginv2.DestinationConfigureRequest {
	out := cpluginv2.DestinationConfigureRequest{}
	if len(in) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = maps.Clone(in)
	}
	return out
}

func DestinationStartRequest() cpluginv2.DestinationStartRequest {
	return cpluginv2.DestinationStartRequest{}
}

func DestinationRunRequest(in opencdc.Record) (cpluginv2.DestinationRunRequest, error) {
	return cpluginv2.DestinationRunRequest{
		Record: in,
	}, nil
}

func DestinationStopRequest(in opencdc.Position) cpluginv2.DestinationStopRequest {
	return cpluginv2.DestinationStopRequest{
		LastPosition: in,
	}
}

func DestinationTeardownRequest() cpluginv2.DestinationTeardownRequest {
	return cpluginv2.DestinationTeardownRequest{}
}

func DestinationLifecycleOnCreatedRequest(cfg map[string]string) cpluginv2.DestinationLifecycleOnCreatedRequest {
	out := cpluginv2.DestinationLifecycleOnCreatedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}

func DestinationLifecycleOnUpdatedRequest(cfgBefore, cfgAfter map[string]string) cpluginv2.DestinationLifecycleOnUpdatedRequest {
	out := cpluginv2.DestinationLifecycleOnUpdatedRequest{}
	// gRPC sends `nil` if the map is empty, match behavior
	if len(cfgBefore) > 0 {
		out.ConfigBefore = cfgBefore
	}
	if len(cfgAfter) > 0 {
		out.ConfigAfter = cfgAfter
	}
	return out
}

func DestinationLifecycleOnDeletedRequest(cfg map[string]string) cpluginv2.DestinationLifecycleOnDeletedRequest {
	out := cpluginv2.DestinationLifecycleOnDeletedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}
