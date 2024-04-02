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

package toproto

import (
	connectorv1 "github.com/conduitio/conduit-connector-protocol/proto/connector/v1"
	"github.com/conduitio/conduit/pkg/record"
)

func SourceConfigureRequest(in map[string]string) *connectorv1.Source_Configure_Request {
	return &connectorv1.Source_Configure_Request{
		Config: in,
	}
}

func SourceStartRequest(in record.Position) *connectorv1.Source_Start_Request {
	return &connectorv1.Source_Start_Request{
		Position: in,
	}
}

func SourceRunRequest(in record.Position) *connectorv1.Source_Run_Request {
	return &connectorv1.Source_Run_Request{
		AckPosition: in,
	}
}

func SourceStopRequest() *connectorv1.Source_Stop_Request {
	return &connectorv1.Source_Stop_Request{}
}

func SourceTeardownRequest() *connectorv1.Source_Teardown_Request {
	return &connectorv1.Source_Teardown_Request{}
}

func SourceLifecycleOnCreatedRequest(cfg map[string]string) *connectorv1.Source_Lifecycle_OnCreated_Request {
	return &connectorv1.Source_Lifecycle_OnCreated_Request{
		Config: cfg,
	}
}

func SourceLifecycleOnUpdatedRequest(cfgBefore, cfgAfter map[string]string) *connectorv1.Source_Lifecycle_OnUpdated_Request {
	return &connectorv1.Source_Lifecycle_OnUpdated_Request{
		ConfigBefore: cfgBefore,
		ConfigAfter:  cfgAfter,
	}
}

func SourceLifecycleOnDeletedRequest(cfg map[string]string) *connectorv1.Source_Lifecycle_OnDeleted_Request {
	return &connectorv1.Source_Lifecycle_OnDeleted_Request{
		Config: cfg,
	}
}
