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
	"github.com/conduitio/conduit/pkg/record"
	connectorv1 "go.buf.build/grpc/go/conduitio/conduit-connector-protocol/connector/v1"
)

func DestinationConfigureRequest(in map[string]string) (*connectorv1.Destination_Configure_Request, error) {
	out := connectorv1.Destination_Configure_Request{
		Config: in,
	}
	return &out, nil
}

func DestinationStartRequest() *connectorv1.Destination_Start_Request {
	return &connectorv1.Destination_Start_Request{}
}

func DestinationRunRequest(in record.Record) (*connectorv1.Destination_Run_Request, error) {
	rec, err := Record(in)
	if err != nil {
		return nil, err
	}
	out := connectorv1.Destination_Run_Request{
		Record: rec,
	}
	return &out, nil
}

func DestinationStopRequest(in record.Position) *connectorv1.Destination_Stop_Request {
	return &connectorv1.Destination_Stop_Request{
		LastPosition: in,
	}
}

func DestinationTeardownRequest() *connectorv1.Destination_Teardown_Request {
	return &connectorv1.Destination_Teardown_Request{}
}
