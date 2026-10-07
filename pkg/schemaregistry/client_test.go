// Copyright © 2023 Meroxa, Inc.
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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestClient_NotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	rtr := newRoundTripRecorder(http.DefaultTransport)
	c, err := NewClient(
		logger,
		sr.HTTPClient(&http.Client{Transport: rtr}),
		sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)),
	)
	is.NoErr(err)

	t.Run("SchemaByID", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		schema, err := c.SchemaByID(ctx, 12345)
		is.True(err != nil)
		is.Equal(sr.Schema{}, schema)

		// check that error is expected
		var respErr *sr.ResponseError
		is.True(cerrors.As(err, &respErr))
		is.Equal(40403, respErr.ErrorCode)

		// check requests made by the client
		is.Equal(len(rtr.Records()), 1)
		rtr.AssertRecord(is, 0,
			assertMethod("GET"),
			assertRequestURI("/schemas/ids/12345"),
			assertResponseStatus(404),
			assertError(nil),
		)
	})

	t.Run("SchemaBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		schema, err := c.SchemaBySubjectVersion(ctx, "not-found", 1)
		is.True(err != nil)
		is.Equal(sr.SubjectSchema{}, schema)

		// check that error is expected
		var respErr *sr.ResponseError
		is.True(cerrors.As(err, &respErr))
		is.Equal(40401, respErr.ErrorCode)

		// check requests made by the client
		is.Equal(len(rtr.Records()), 1)
		rtr.AssertRecord(is, 0,
			assertMethod("GET"),
			assertRequestURI("/subjects/not-found/versions/1"),
			assertResponseStatus(404),
			assertError(nil),
		)
	})
}

func TestClient_CacheMiss(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	// register schema in the schema registry but not in the client, to get a
	// cache miss but fetch from registry should return the schema

	srClient, err := sr.NewClient(sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	want, err := srClient.CreateSchema(ctx, "test-cache-miss", sr.Schema{
		Schema: `"string"`,
		Type:   sr.TypeAvro,
	})
	is.NoErr(err)

	// now try fetching schema with our cached client

	rtr := newRoundTripRecorder(http.DefaultTransport)
	c, err := NewClient(
		logger,
		sr.HTTPClient(&http.Client{Transport: rtr}),
		sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)),
	)
	is.NoErr(err)

	t.Run("SchemaByID", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		got, err := c.SchemaByID(ctx, want.ID)
		is.NoErr(err)
		is.Equal(want.Schema, got)

		// check requests made by the client
		is.Equal(len(rtr.Records()), 1)
		rtr.AssertRecord(is, 0,
			assertMethod("GET"),
			assertRequestURI(fmt.Sprintf("/schemas/ids/%d", want.ID)),
			assertResponseStatus(200),
			assertError(nil),
		)

		// fetching the schema again should hit the cache
		rtr.Clear()
		got, err = c.SchemaByID(ctx, want.ID)
		is.NoErr(err)
		is.Equal(want.Schema, got)
		is.Equal(len(rtr.Records()), 0)
	})

	// SchemaBySubjectVersion should also report a cache miss, because
	// SchemaByID only returns a sr.Schema so the cache does not contain the
	// full info

	t.Run("SchemaBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		got, err := c.SchemaBySubjectVersion(ctx, want.Subject, want.Version)
		is.NoErr(err)
		is.Equal(want, got)

		// check requests made by the client
		is.Equal(len(rtr.Records()), 1)
		rtr.AssertRecord(is, 0,
			assertMethod("GET"),
			assertRequestURI(fmt.Sprintf("/subjects/%s/versions/%d", want.Subject, want.Version)),
			assertResponseStatus(200),
			assertError(nil),
		)

		// fetching the schema again should hit the cache
		rtr.Clear()
		got, err = c.SchemaBySubjectVersion(ctx, want.Subject, want.Version)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(len(rtr.Records()), 0)
	})
}

func TestClient_CacheHit(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	// register schema in the schema registry using the client, it should cache
	// the schema so no further requests are made when retrieving the schema

	rtr := newRoundTripRecorder(http.DefaultTransport)
	c, err := NewClient(
		logger,
		sr.HTTPClient(&http.Client{Transport: rtr}),
		sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)),
	)
	is.NoErr(err)

	// The assertions below expect exactly 5 requests in a fixed order, which
	// only holds if both the subject and the schema are new to the registry:
	//   - CreateSchema sets compatibility (the PUT /config request) only when
	//     it registers a new subject;
	//   - sr.Client.CreateSchema issues one GET /subjects/<s>/versions/<v> per
	//     subject that already uses the schema ID, concurrently.
	// Under -tags integration the registry is a real server that outlives a
	// single run, so a fixed subject and schema made every repetition after
	// the first (-count=3 in the flake hunt) see 4 requests instead of 5.
	id := uniqueID(t)
	subject := "test-cache-hit-" + id
	want, err := c.CreateSchema(ctx, subject, sr.Schema{
		Schema: fmt.Sprintf(`{"type":"record","name":"CacheHit_%s","fields":[{"name":"f","type":"int"}]}`, id),
		Type:   sr.TypeAvro,
	})
	is.NoErr(err)

	is.Equal(len(rtr.Records()), 5)
	rtr.AssertRecord(is, 0,
		assertMethod("GET"),
		assertRequestURI(fmt.Sprintf("/subjects/%s/versions?deleted=true", subject)),
		assertResponseStatus(404),
		assertError(nil),
	)
	rtr.AssertRecord(is, 1,
		assertMethod("POST"),
		assertRequestURI(fmt.Sprintf("/subjects/%s/versions", subject)),
		assertResponseStatus(200),
		assertError(nil),
	)
	rtr.AssertRecord(is, 2,
		assertMethod("GET"),
		assertRequestURI(fmt.Sprintf("/schemas/ids/%d/versions", want.ID)),
		assertResponseStatus(200),
		assertError(nil),
	)
	rtr.AssertRecord(is, 3,
		assertMethod("GET"),
		assertRequestURI(fmt.Sprintf("/subjects/%s/versions/%d", subject, want.Version)),
		assertResponseStatus(200),
		assertError(nil),
	)
	rtr.AssertRecord(is, 4,
		assertMethod("PUT"),
		assertRequestURI(fmt.Sprintf("/config/%s", subject)),
		assertResponseStatus(200),
		assertError(nil),
	)

	rtr.Clear() // clear requests before subtests

	t.Run("SchemaByID", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		got, err := c.SchemaByID(ctx, want.ID)
		is.NoErr(err)
		is.Equal(want.Schema, got)

		// schema should have been retrieved from the cache
		is.Equal(len(rtr.Records()), 0)
	})

	t.Run("SchemaBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		defer rtr.Clear() // clear requests after test

		got, err := c.SchemaBySubjectVersion(ctx, want.Subject, want.Version)
		is.NoErr(err)
		is.Equal(want, got)

		// schema should have been retrieved from the cache
		is.Equal(len(rtr.Records()), 0)
	})
}

// uniqueID returns a random hex string, usable in both subject names and Avro
// names, for tests that need a subject or schema the registry has not seen.
func uniqueID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to generate random ID: %v", err)
	}
	return hex.EncodeToString(b)
}

// roundTripRecorder wraps a http.RoundTripper and records all requests and
// responses going through it. It also provides utility methods to assert the
// records. It is safe for concurrent use.
type roundTripRecorder struct {
	rt      http.RoundTripper
	records []*roundTripRecord
	m       sync.Mutex
}

// roundTripRecord records a single round trip.
type roundTripRecord struct {
	Request  *http.Request
	Response *http.Response
	Error    error
}

func newRoundTripRecorder(rt http.RoundTripper) *roundTripRecorder {
	return &roundTripRecorder{
		rt:      rt,
		records: make([]*roundTripRecord, 0),
	}
}

func (r *roundTripRecorder) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// Records are stored by pointer: sr.Client issues some requests
	// concurrently, and a pointer into a []roundTripRecord would be left
	// aimed at the old backing array when a concurrent append reallocates,
	// silently losing the response.
	rec := &roundTripRecord{Request: req}
	r.m.Lock()
	r.records = append(r.records, rec)
	r.m.Unlock()

	defer func() {
		r.m.Lock()
		defer r.m.Unlock()
		rec.Response = resp
		rec.Error = err
	}()
	return r.rt.RoundTrip(req)
}

func (r *roundTripRecorder) Records() []*roundTripRecord {
	r.m.Lock()
	defer r.m.Unlock()
	return r.records
}

func (r *roundTripRecorder) Clear() {
	r.m.Lock()
	defer r.m.Unlock()
	r.records = make([]*roundTripRecord, 0)
}

func (r *roundTripRecorder) AssertRecord(is *is.I, index int, asserters ...roundTripRecordAsserter) {
	r.m.Lock()
	defer r.m.Unlock()

	is.Helper()
	is.True(len(r.records) > index) // record with index does not exist
	rec := *r.records[index]
	for _, assert := range asserters {
		assert(is, rec)
	}
}

type roundTripRecordAsserter func(*is.I, roundTripRecord)

func assertMethod(method string) roundTripRecordAsserter {
	return func(is *is.I, rec roundTripRecord) {
		is.Helper()
		is.Equal(method, rec.Request.Method) // unexpected method
	}
}

func assertRequestURI(uri string) roundTripRecordAsserter {
	return func(is *is.I, rec roundTripRecord) {
		is.Helper()
		is.Equal(uri, rec.Request.URL.RequestURI()) // unexpected request URI
	}
}

func assertResponseStatus(code int) roundTripRecordAsserter {
	return func(is *is.I, rec roundTripRecord) {
		is.Helper()
		is.Equal(code, rec.Response.StatusCode) // unexpected response status
	}
}

func assertError(err error) roundTripRecordAsserter {
	return func(is *is.I, rec roundTripRecord) {
		is.Helper()
		is.Equal(err, rec.Error) // unexpected error
	}
}
