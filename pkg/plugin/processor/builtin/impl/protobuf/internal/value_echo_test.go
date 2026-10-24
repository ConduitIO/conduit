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

package internal

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// A Protobuf codec error can quote the record value; errors reach the DLQ, the
// API and CLI output, so the decoder reports only the type of the error.
func TestDecoder_CodecErrorDoesNotContainTheValue(t *testing.T) {
	is := is.New(t)
	d := NewDecoder(&fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string s = 1; }`,
	}}, log.Nop())

	// field 1 claims 60 bytes and carries a short secret
	payload := append([]byte{0x0A, 60}, []byte("customer-SECRET-4711")...)
	_, err := d.Decode(context.Background(), opencdc.RawData(wire(t, 1, []int{0}, payload)))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "failed to unmarshal data with schema"))
	is.True(strings.Contains(err.Error(), "details not shown"))
	is.True(!strings.Contains(err.Error(), "SECRET"))
}
