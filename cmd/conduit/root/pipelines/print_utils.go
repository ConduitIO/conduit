// Copyright © 2025 Meroxa, Inc.
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

package pipelines

import (
	"strings"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func indentation(level int) string {
	return strings.Repeat("  ", level)
}

func getPipelineStatus(pipeline *apiv1.Pipeline) string {
	return prettyProtoEnum("STATUS_", pipeline.State.Status.String())
}

func prettyProtoEnum(prefix, protoEnum string) string {
	return strings.ToLower(
		strings.ReplaceAll(protoEnum, prefix, ""),
	)
}

func printTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().Format("2006-01-02T15:04:05Z")
}