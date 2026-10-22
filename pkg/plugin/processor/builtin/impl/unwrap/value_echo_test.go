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

package unwrap

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// The operation comes from the record; the error must not repeat it.
func TestDebezium_InvalidOperationDoesNotEchoTheValue(t *testing.T) {
	_, err := NewDebeziumProcessor(log.Nop()).convertOperation("SECRET-op")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error repeats the operation: %v", err)
	}
}
