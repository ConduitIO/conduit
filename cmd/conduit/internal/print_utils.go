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

package internal

import (
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func Indentation(level int) string {
	return strings.Repeat("  ", level)
}

func PrintStatusFromProtoString(protoStatus string) string {
	return PrettyProtoEnum("STATUS_", protoStatus)
}

func PrettyProtoEnum(prefix, protoEnum string) string {
	return strings.ToLower(
		strings.ReplaceAll(protoEnum, prefix, ""),
	)
}

func PrintTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().Format("2006-01-02T15:04:05Z")
}
