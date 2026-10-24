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
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/matryer/is"
	"github.com/rs/zerolog"
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

// The warning for a failed SetCompatibility carries the error of franz-go's
// client, which formats the registry URL into its text.
func TestClient_SetCompatibilityWarningDoesNotContainTheRegistryURLCredentials(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schemas/ids/1/versions":
			_, _ = w.Write([]byte(`[{"subject":"subject-1","version":1}]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/subjects/subject-1/versions/"):
			_, _ = w.Write([]byte(`{"subject":"subject-1","version":1,"id":1,"schema":` + strconv.Quote(orderSchema) + `}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_code":40401,"message":"not found"}`))
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":1}`))
		default: // the compatibility update fails at the connection, so the client's error holds the URL
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	logger := log.New(zerolog.New(&buf))
	url := strings.Replace(srv.URL, "http://", "http://SECRET-user:SECRET-pass@", 1) + "/?token=SECRET-token"
	c, err := NewClient(logger, sr.URLs(url))
	is.NoErr(err)

	_, err = c.CreateSchema(context.Background(), "subject-1", sr.Schema{Schema: orderSchema})
	is.NoErr(err)
	is.True(strings.Contains(buf.String(), "failed to set compatibility to none"))
	is.True(!strings.Contains(buf.String(), "SECRET"))
}
