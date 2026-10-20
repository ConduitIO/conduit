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

package schema

import (
	"strings"
	"testing"

	"github.com/matryer/is"
)

const minimal = `{"reportVersion": 1, "generator": {"conduit": "v0.21.0", "rules": []},
 "summary": {"connectors": 0, "keys": 0, "translated": 0, "approximated": 0, "unsupported": 0, "blocking": 0},
 "connectors": [], "acceptedGaps": []}`

func TestValidateReport(t *testing.T) {
	is := is.New(t)
	is.NoErr(ValidateReport([]byte(minimal)))

	err := ValidateReport([]byte(strings.Replace(minimal, `"reportVersion": 1`, `"reportVersion": 2`, 1)))
	is.True(err != nil && strings.Contains(err.Error(), ReportV1ID))

	is.True(ValidateReport([]byte(`{`)) != nil)
}
