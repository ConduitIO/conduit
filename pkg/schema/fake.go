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

//go:build !integration

package schema

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

var (
	fakeServerByTest     = make(map[string]*httptest.Server)
	fakeServerByTestLock sync.Mutex
)

// ExampleSchemaRegistryURL creates a fake in-memory schema registry server and
// returns its address and a cleanup function which should be executed in a
// deferred call.
//
// This method is only used if examples are run without --tags=integration. It
// is meant as a utility to allow faster iteration when developing, please run
// integration tests to ensure the code works with a real schema registry.
func ExampleSchemaRegistryURL(exampleName string, port int) (string, func()) {
	// discard all schema registry logs in examples
	logf := func(_ string, _ ...any) {}
	return fakeSchemaRegistryURL(exampleName, logf, port)
}

// TestSchemaRegistryURL creates a fake in-memory schema registry server and
// returns its address.
//
// This method is only used if the tests are run without
// --tags=integration. It is meant as a utility to allow faster iteration when
// developing, please run integration tests to ensure the code works with a real
// schema registry.
func TestSchemaRegistryURL(t testing.TB) string {
	url, cleanup := fakeSchemaRegistryURL(t.Name(), t.Logf, 0)
	t.Cleanup(cleanup)
	return url
}

func fakeSchemaRegistryURL(name string, logf func(format string, args ...any), port int) (string, func()) {
	fakeServerByTestLock.Lock()
	defer fakeServerByTestLock.Unlock()

	srv := fakeServerByTest[name]
	cleanup := func() {}
	if srv == nil {
		srv = httptest.NewUnstartedServer(newFakeServer(logf))
		if port > 0 {
			// NewUnstartedServer creates a listener. Close that listener and replace
			// with a custom one.
			_ = srv.Listener.Close()
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				panic(fmt.Sprintf("failed starting test server on port %d: %v", port, err))
			}
			srv.Listener = l
		}

		srv.Start()
		fakeServerByTest[name] = srv
		cleanup = srv.Close
	}
	return srv.URL, cleanup
}

const (
	errorCodeSubjectNotFound = 40401
	errorCodeSchemaNotFound  = 40403
)

// FakeRegistry is a simple fake registry meant to be used in tests. It stores
// schemas in memory and supports only the basic functionality needed in our
// tests and supported by our client.
type FakeRegistry struct {
	schemas            []sr.SubjectSchema
	fingerprintIDCache map[uint64]int
	idSequence         int

	m sync.Mutex
}

func NewFakeRegistry() *FakeRegistry {
	return &FakeRegistry{
		schemas:            make([]sr.SubjectSchema, 0),
		fingerprintIDCache: make(map[uint64]int),
	}
}

func (fr *FakeRegistry) CreateSchema(subject string, schema sr.Schema) sr.SubjectSchema {
	fr.m.Lock()
	defer fr.m.Unlock()

	fp := Rabin([]byte(schema.Schema))
	id, ok := fr.fingerprintIDCache[fp]
	if ok {
		// schema exists, see if subject matches
		ss, ok := fr.findBySubjectID(subject, id)
		if ok {
			// schema exists for this subject, return it
			return ss
		}
	}
	if !ok {
		// schema does not exist yet
		id = fr.nextID()
	}
	version := fr.nextVersion(subject)

	ss := sr.SubjectSchema{
		Subject: subject,
		Version: version,
		ID:      id,
		Schema:  schema,
	}

	fr.schemas = append(fr.schemas, ss)
	fr.fingerprintIDCache[fp] = id

	return ss
}

func (fr *FakeRegistry) SchemaByID(id int) (sr.Schema, bool) {
	fr.m.Lock()
	defer fr.m.Unlock()

	s, ok := fr.findOneByID(id)
	return s, ok
}

func (fr *FakeRegistry) SchemaBySubjectVersion(subject string, version int) (sr.SubjectSchema, bool) {
	fr.m.Lock()
	defer fr.m.Unlock()

	return fr.findBySubjectVersion(subject, version)
}

func (fr *FakeRegistry) SubjectVersionsByID(id int) []sr.SubjectSchema {
	fr.m.Lock()
	defer fr.m.Unlock()

	return fr.findAllByID(id)
}

func (fr *FakeRegistry) nextID() int {
	fr.idSequence++
	return fr.idSequence
}

func (fr *FakeRegistry) nextVersion(subject string) int {
	return len(fr.findBySubject(subject)) + 1
}

func (fr *FakeRegistry) findBySubject(subject string) []sr.SubjectSchema {
	var sss []sr.SubjectSchema
	for _, ss := range fr.schemas {
		if ss.Subject == subject {
			sss = append(sss, ss)
		}
	}
	return sss
}

func (fr *FakeRegistry) findOneByID(id int) (sr.Schema, bool) {
	for _, ss := range fr.schemas {
		if ss.ID == id {
			return ss.Schema, true
		}
	}
	return sr.Schema{}, false
}

func (fr *FakeRegistry) findAllByID(id int) []sr.SubjectSchema {
	var sss []sr.SubjectSchema
	for _, ss := range fr.schemas {
		if ss.ID == id {
			sss = append(sss, ss)
		}
	}
	return sss
}

func (fr *FakeRegistry) findBySubjectID(subject string, id int) (sr.SubjectSchema, bool) {
	for _, ss := range fr.schemas {
		if ss.Subject == subject && ss.ID == id {
			return ss, true
		}
	}
	return sr.SubjectSchema{}, false
}

func (fr *FakeRegistry) findBySubjectVersion(subject string, version int) (sr.SubjectSchema, bool) {
	for _, ss := range fr.schemas {
		if ss.Subject == subject && ss.Version == version {
			return ss, true
		}
	}
	return sr.SubjectSchema{}, false
}

// fakeServer is a fake schema registry server.
type fakeServer struct {
	fr   *FakeRegistry
	logf func(format string, args ...any)
}

func newFakeServer(logf func(format string, args ...any)) *fakeServer {
	fs := &fakeServer{
		fr:   NewFakeRegistry(),
		logf: func(format string, args ...any) { /* no op */ },
	}
	if logf != nil {
		fs.logf = logf
	}
	return fs
}

func (fs *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fs.logf("%s %s", r.Method, r.RequestURI)

	var (
		id      int
		subject string
		version int
	)
	p := r.URL.Path
	switch {
	case fs.match(p, "/schemas/ids/+", &id) && r.Method == http.MethodGet:
		fs.schemaByID(w, r, id)
	case fs.match(p, "/schemas/ids/+/versions", &id) && r.Method == http.MethodGet:
		fs.subjectVersionsByID(w, r, id)
	case fs.match(p, "/subjects/+/versions", &subject) && r.Method == http.MethodPost:
		fs.createSchema(w, r, subject)
	case fs.match(p, "/subjects/+/versions/+", &subject, &version) && r.Method == http.MethodGet:
		fs.schemaBySubjectVersion(w, r, subject, version)
	case fs.match(p, "/config/+", &subject) && r.Method == http.MethodPut:
		fs.updateConfig(w, r)
	default:
		http.NotFound(w, r)
	}
}

// match reports whether path matches the given pattern, which is a
// path with '+' wildcards wherever you want to use a parameter. Path
// parameters are assigned to the pointers in vars (len(vars) must be
// the number of wildcards), which must be of type *string or *int.
// Source: https://github.com/benhoyt/go-routing/blob/master/match/route.go
func (*fakeServer) match(path, pattern string, vars ...interface{}) bool {
	for ; pattern != "" && path != ""; pattern = pattern[1:] {
		switch pattern[0] {
		case '+':
			// '+' matches till next slash in path
			slash := strings.IndexByte(path, '/')
			if slash < 0 {
				slash = len(path)
			}
			segment := path[:slash]
			path = path[slash:]
			switch p := vars[0].(type) {
			case *string:
				*p = segment
			case *int:
				n, err := strconv.Atoi(segment)
				if err != nil || n < 0 {
					return false
				}
				*p = n
			default:
				panic("vars must be *string or *int")
			}
			vars = vars[1:]
		case path[0]:
			// non-'+' pattern byte must match path byte
			path = path[1:]
		default:
			return false
		}
	}
	return path == "" && pattern == ""
}

func (fs *fakeServer) createSchema(w http.ResponseWriter, r *http.Request, subject string) {
	// POST /subjects/{subject}/versions => returns ID
	defer r.Body.Close()
	var s sr.Schema
	err := json.NewDecoder(r.Body).Decode(&s)
	if err != nil {
		fs.error(w, http.StatusInternalServerError, err)
		return
	}

	ss := fs.fr.CreateSchema(subject, s)
	fs.json(w, map[string]any{"id": ss.ID})
}

func (fs *fakeServer) schemaBySubjectVersion(w http.ResponseWriter, _ *http.Request, subject string, version int) {
	// GET /subjects/{subject}/versions/{version}
	ss, ok := fs.fr.SchemaBySubjectVersion(subject, version)
	if !ok {
		fs.errorWithCode(w, http.StatusNotFound, errorCodeSubjectNotFound, cerrors.New("subject not found"))
		return
	}
	fs.json(w, ss)
}

func (fs *fakeServer) schemaByID(w http.ResponseWriter, _ *http.Request, id int) {
	// GET /schemas/ids/{id}
	s, ok := fs.fr.SchemaByID(id)
	if !ok {
		fs.errorWithCode(w, http.StatusNotFound, errorCodeSchemaNotFound, cerrors.New("schema not found"))
		return
	}
	fs.json(w, s)
}

func (fs *fakeServer) subjectVersionsByID(w http.ResponseWriter, _ *http.Request, id int) {
	// GET /schemas/ids/{id}/versions
	sss := fs.fr.SubjectVersionsByID(id)
	fs.json(w, sss)
}

func (fs *fakeServer) updateConfig(w http.ResponseWriter, r *http.Request) {
	// PUT /config/{subject}
	defer r.Body.Close()
	var c struct {
		Compatibility string `json:"compatibility"`
	}
	err := json.NewDecoder(r.Body).Decode(&c)
	if err != nil {
		fs.error(w, http.StatusInternalServerError, err)
		return
	}

	valid := map[string]bool{
		"BACKWARD":            true,
		"BACKWARD_TRANSITIVE": true,
		"FORWARD":             true,
		"FORWARD_TRANSITIVE":  true,
		"FULL":                true,
		"FULL_TRANSITIVE":     true,
		"NONE":                true,
	}[c.Compatibility]
	if !valid {
		fs.errorWithCode(w, 42203, http.StatusUnprocessableEntity, cerrors.New("invalid compatibility level"))
		return
	}
	fs.json(w, c)
}

func (fs *fakeServer) json(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fs.error(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = w.Write(b)
}

func (fs *fakeServer) error(w http.ResponseWriter, status int, err error) {
	fs.errorWithCode(w, status, 50001, err)
}

func (fs *fakeServer) errorWithCode(w http.ResponseWriter, status int, code int, err error) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error_code": code,
		"message":    err.Error(),
	})
}
