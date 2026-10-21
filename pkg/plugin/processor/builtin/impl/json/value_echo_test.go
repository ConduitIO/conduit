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

package json

import (
	stdjson "encoding/json"
	"strings"
	"testing"
)

// A number outside the float64 range comes from record data; the error must
// not repeat it.
func TestDecodeJSONNumber_ErrorDoesNotEchoTheNumber(t *testing.T) {
	_, err := decodeJSONNumber(stdjson.Number("1" + strings.Repeat("0", 400) + "e5"))
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "0000000000") {
		t.Errorf("error repeats the number: %.120s", err)
	}
}
