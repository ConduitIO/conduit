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

package fromproto

import (
	"github.com/conduitio/conduit/pkg/record"
	connectorv1 "go.buf.build/library/go-grpc/conduitio/conduit-connector-protocol/connector/v1"
)

func SourceRunResponse(in *connectorv1.Source_Run_Response) (record.Record, error) {
	out, err := Record(in.Record)
	if err != nil {
		return record.Record{}, nil
	}
	return out, nil
}

func SourceStopResponse(in *connectorv1.Source_Stop_Response) (record.Position, error) {
	return in.LastPosition, nil
}
