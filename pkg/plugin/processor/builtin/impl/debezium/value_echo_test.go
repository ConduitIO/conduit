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

package debezium

import (
	"strings"
	"testing"
)

// Decimal literals come from record fields; errors must not repeat them.
func TestDecimalUnscaled_ErrorsDoNotEchoTheLiteral(t *testing.T) {
	for _, lit := range []string{"SECRET-4711", "1.23456789SECRET"} {
		_, err := decimalUnscaled(lit, 2)
		if err == nil {
			t.Fatalf("%q: want an error", lit)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error repeats the literal: %v", err)
		}
	}
}
