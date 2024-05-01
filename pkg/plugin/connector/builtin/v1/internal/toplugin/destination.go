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

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/record"
)

func DestinationConfigureRequest(in map[string]string) cpluginv1.DestinationConfigureRequest {
	out := cpluginv1.DestinationConfigureRequest{}
	if len(in) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = maps.Clone(in)
	}
	return out
}

func DestinationStartRequest() cpluginv1.DestinationStartRequest {
	return cpluginv1.DestinationStartRequest{}
}

func DestinationRunRequest(in record.Record) (cpluginv1.DestinationRunRequest, error) {
	rec, err := Record(in)
	if err != nil {
		return cpluginv1.DestinationRunRequest{}, err
	}

	out := cpluginv1.DestinationRunRequest{
		Record: rec,
	}
	return out, nil
}

func DestinationStopRequest(in record.Position) cpluginv1.DestinationStopRequest {
	return cpluginv1.DestinationStopRequest{
		LastPosition: in,
	}
}

func DestinationTeardownRequest() cpluginv1.DestinationTeardownRequest {
	return cpluginv1.DestinationTeardownRequest{}
}

func DestinationLifecycleOnCreatedRequest(cfg map[string]string) cpluginv1.DestinationLifecycleOnCreatedRequest {
	out := cpluginv1.DestinationLifecycleOnCreatedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}

func DestinationLifecycleOnUpdatedRequest(cfgBefore, cfgAfter map[string]string) cpluginv1.DestinationLifecycleOnUpdatedRequest {
	out := cpluginv1.DestinationLifecycleOnUpdatedRequest{}
	// gRPC sends `nil` if the map is empty, match behavior
	if len(cfgBefore) > 0 {
		out.ConfigBefore = cfgBefore
	}
	if len(cfgAfter) > 0 {
		out.ConfigAfter = cfgAfter
	}
	return out
}

func DestinationLifecycleOnDeletedRequest(cfg map[string]string) cpluginv1.DestinationLifecycleOnDeletedRequest {
	out := cpluginv1.DestinationLifecycleOnDeletedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}
