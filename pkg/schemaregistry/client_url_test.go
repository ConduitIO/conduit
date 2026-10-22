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

package schemaregistry

import (
	"context"
	"strings"
	"testing"

	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// The registry URL can carry credentials (user info, a token in the query) and
// franz-go's client formats it into its error text. Errors reach the DLQ, the
// API and CLI output.
func TestClient_ErrorsDoNotContainTheRegistryURLCredentials(t *testing.T) {
	is := is.New(t)
	c, err := NewClient(log.Nop(), sr.URLs("http://SECRET-user:SECRET-pass@127.0.0.1:1/?token=SECRET-token"))
	is.NoErr(err)

	err = c.Check(context.Background())
	is.True(err != nil)
	is.True(!strings.Contains(err.Error(), "SECRET"))

	_, err = c.SchemaByID(context.Background(), 3)
	is.True(err != nil)
	is.True(!strings.Contains(err.Error(), "SECRET"))
}
