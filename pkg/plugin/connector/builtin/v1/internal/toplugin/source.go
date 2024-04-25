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

func SourceConfigureRequest(in map[string]string) cpluginv1.SourceConfigureRequest {
	out := cpluginv1.SourceConfigureRequest{}
	if len(in) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = maps.Clone(in)
	}
	return out
}

func SourceStartRequest(in record.Position) cpluginv1.SourceStartRequest {
	return cpluginv1.SourceStartRequest{
		Position: in,
	}
}

func SourceRunRequest(in record.Position) cpluginv1.SourceRunRequest {
	return cpluginv1.SourceRunRequest{
		AckPosition: in,
	}
}

func SourceStopRequest() cpluginv1.SourceStopRequest {
	return cpluginv1.SourceStopRequest{}
}

func SourceTeardownRequest() cpluginv1.SourceTeardownRequest {
	return cpluginv1.SourceTeardownRequest{}
}

func SourceLifecycleOnCreatedRequest(cfg map[string]string) cpluginv1.SourceLifecycleOnCreatedRequest {
	out := cpluginv1.SourceLifecycleOnCreatedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}

func SourceLifecycleOnUpdatedRequest(cfgBefore, cfgAfter map[string]string) cpluginv1.SourceLifecycleOnUpdatedRequest {
	out := cpluginv1.SourceLifecycleOnUpdatedRequest{}
	// gRPC sends `nil` if the map is empty, match behavior
	if len(cfgBefore) > 0 {
		out.ConfigBefore = cfgBefore
	}
	if len(cfgAfter) > 0 {
		out.ConfigAfter = cfgAfter
	}
	return out
}

func SourceLifecycleOnDeletedRequest(cfg map[string]string) cpluginv1.SourceLifecycleOnDeletedRequest {
	out := cpluginv1.SourceLifecycleOnDeletedRequest{}
	if len(cfg) > 0 {
		// gRPC sends `nil` if the map is empty, match behavior
		out.Config = cfg
	}
	return out
}
