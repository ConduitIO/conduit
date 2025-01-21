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
	"fmt"
	"strings"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Indentation returns a string with the number of spaces equal to the level
func Indentation(level int) string {
	return strings.Repeat("  ", level)
}

// PrintStatusFromProtoString returns a human-readable status from a proto status
func PrintStatusFromProtoString(protoStatus string) string {
	return PrettyProtoEnum("STATUS_", protoStatus)
}

// PrettyProtoEnum returns a human-readable string from a proto enum
func PrettyProtoEnum(prefix, protoEnum string) string {
	return strings.ToLower(
		strings.TrimPrefix(protoEnum, prefix),
	)
}

// PrintTime returns a human-readable time from a timestamp
func PrintTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().Format("2006-01-02T15:04:05Z")
}

// DisplayProcessors prints the processors in a human-readable format
func DisplayProcessors(processors []*apiv1.Processor, indent int) {
	if len(processors) == 0 {
		return
	}

	fmt.Printf("%sProcessors:\n", Indentation(indent))

	for _, p := range processors {
		fmt.Printf("%s- ID: %s\n", Indentation(indent+1), p.Id)
		fmt.Printf("%sPlugin: %s\n", Indentation(indent+2), p.Plugin)

		if p.Condition != "" {
			fmt.Printf("%sCondition: %s\n", Indentation(indent+2), p.Condition)
		}

		fmt.Printf("%sConfig:\n", Indentation(indent+2))
		for name, value := range p.Config.Settings {
			fmt.Printf("%s%s: %s\n", Indentation(indent+3), name, value)
		}
		fmt.Printf("%sWorkers: %d\n", Indentation(indent+3), p.Config.Workers)

		fmt.Printf("%sCreated At: %s\n", Indentation(indent+2), PrintTime(p.CreatedAt))
		fmt.Printf("%sUpdated At: %s\n", Indentation(indent+2), PrintTime(p.UpdatedAt))
	}
}
