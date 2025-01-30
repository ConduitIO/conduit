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

package testutils

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetDateTime returns a sample timestamppb.Timestamp that can be used by API mocks.
func GetDateTime() *timestamppb.Timestamp {
	parsedTime, _ := time.Parse(time.RFC3339, "1970-01-01T00:00:00Z")
	return timestamppb.New(parsedTime)
}
