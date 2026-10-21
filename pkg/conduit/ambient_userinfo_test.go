// Copyright © 2026 Meroxa, Inc.
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

package conduit

import (
	"strings"
	"testing"

	"github.com/matryer/is"
)

// TestParseAmbientEndpoint_RefusalDoesNotEchoUserInfo: an entry with user
// info is refused because it carries credentials; the refusal must not print
// them back.
func TestParseAmbientEndpoint_RefusalDoesNotEchoUserInfo(t *testing.T) {
	is := is.New(t)
	_, err := parseAmbientEndpoint("https://SECRET-user:SECRET-pass@s3.example.com")
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "no user info"))
	is.True(strings.Contains(err.Error(), "s3.example.com"))
	is.True(!strings.Contains(err.Error(), "SECRET"))
}
