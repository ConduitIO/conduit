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
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/rabin"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// forgeCollidingAvroSchema returns Avro schema text that differs from victim in
// type and has the same Rabin fingerprint.
//
// Rabin is a CRC, so it is linear over GF(2): flipping an input bit XORs a
// fixed 64-bit vector into the fingerprint. The forgery hides 40 letters in
// the "doc" attribute (each one of 16 letters, so 4 free bits), then solves
// for the letters that steer the fingerprint onto the victim's by Gaussian
// elimination. The result is ordinary, parseable schema text.
//
// Same technique used against conduit-commons' serde cache
// (GHSA-94xc-rjcp-83cr) and conduit-schema-registry's fingerprintIDCache;
// reproduced here against this package's old subjectFingerprintCache.
func forgeCollidingAvroSchema(t *testing.T, victim []byte, name string) []byte {
	t.Helper()

	const letters = 40
	prefix := `{"type":"record","name":"` + name + `","doc":"`
	suffix := `","fields":[{"name":"id","type":"bytes"},{"name":"amount_cents","type":"string"}]}`
	build := func(bits [letters * 4]bool) []byte {
		var b bytes.Buffer
		b.WriteString(prefix)
		for i := range letters {
			var n byte
			for j := range 4 {
				if bits[i*4+j] {
					n |= 1 << j
				}
			}
			b.WriteByte('`' + n) // '`' (0x60) ... 'o' (0x6f), all safe in a JSON string
		}
		b.WriteString(suffix)
		return b.Bytes()
	}

	var zero [letters * 4]bool
	base := rabin.Bytes(build(zero))
	type row struct {
		v    uint64
		comb [3]uint64 // which input bits were XORed to produce v
	}
	var basis [64]*row
	for i := range zero {
		var bits [letters * 4]bool
		bits[i] = true
		r := &row{v: rabin.Bytes(build(bits)) ^ base}
		r.comb[i/64] |= 1 << (i % 64)
		for bit := 63; bit >= 0 && r.v != 0; bit-- {
			if r.v>>bit&1 == 0 {
				continue
			}
			if basis[bit] == nil {
				basis[bit] = r
				break
			}
			r.v ^= basis[bit].v
			for k := range r.comb {
				r.comb[k] ^= basis[bit].comb[k]
			}
		}
	}

	want := rabin.Bytes(victim) ^ base
	var pick [3]uint64
	for bit := 63; bit >= 0; bit-- {
		if want>>bit&1 == 0 {
			continue
		}
		if basis[bit] == nil {
			t.Fatalf("cannot solve for fingerprint bit %d", bit)
		}
		want ^= basis[bit].v
		for k := range pick {
			pick[k] ^= basis[bit].comb[k]
		}
	}
	var bits [letters * 4]bool
	for i := range bits {
		bits[i] = pick[i/64]>>(i%64)&1 == 1
	}
	return build(bits)
}

// fakeRegistry is a minimal, correct schema registry: identity is the exact
// (type, references, text) tuple, so it never confuses two schemas the way the
// v0.2.6 builtin registry does. It lets the client tests run hermetically,
// independent of the conduit-schema-registry version in go.mod.
type fakeRegistry struct {
	m        sync.Mutex
	requests int
	url      string
	failPOST int // number of upcoming POSTs to answer with 503

	ids      map[string]int    // schema identity -> id
	schemas  map[int]sr.Schema // id -> schema
	versions map[string][]int  // subject -> ids, version = index+1
}

func newFakeRegistry(t *testing.T) (*fakeRegistry, string) {
	t.Helper()
	f := &fakeRegistry{
		ids:      make(map[string]int),
		schemas:  make(map[int]sr.Schema),
		versions: make(map[string][]int),
	}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/vnd.schemaregistry.v1+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func(w http.ResponseWriter, code int) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error_code": code, "message": "not found"})
	}

	mux.HandleFunc("GET /subjects/{subject}/versions", func(w http.ResponseWriter, r *http.Request) {
		vs := f.versions[r.PathValue("subject")]
		if len(vs) == 0 {
			notFound(w, 40401)
			return
		}
		out := make([]int, len(vs))
		for i := range vs {
			out[i] = i + 1
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /subjects/{subject}/versions", func(w http.ResponseWriter, r *http.Request) {
		if f.failPOST > 0 {
			f.failPOST--
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error_code": 50003, "message": "unavailable"})
			return
		}
		var s sr.Schema
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error_code": 42201, "message": err.Error()})
			return
		}
		key := string(digestForFake(s))
		id, ok := f.ids[key]
		if !ok {
			id = len(f.ids) + 1
			f.ids[key] = id
			f.schemas[id] = s
		}
		subject := r.PathValue("subject")
		exists := false
		for _, v := range f.versions[subject] {
			exists = exists || v == id
		}
		if !exists {
			f.versions[subject] = append(f.versions[subject], id)
		}
		writeJSON(w, http.StatusOK, map[string]int{"id": id})
	})
	mux.HandleFunc("PUT /config/{subject}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"compatibility": "NONE"})
	})
	mux.HandleFunc("GET /schemas/ids/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		type sv struct {
			Subject string `json:"subject"`
			Version int    `json:"version"`
		}
		var out []sv
		for subject, ids := range f.versions {
			for i, got := range ids {
				if got == id {
					out = append(out, sv{subject, i + 1})
				}
			}
		}
		if len(out) == 0 {
			notFound(w, 40403)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /schemas/ids/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		s, ok := f.schemas[id]
		if !ok {
			notFound(w, 40403)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("GET /subjects/{subject}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		subject := r.PathValue("subject")
		v, _ := strconv.Atoi(r.PathValue("version"))
		ids := f.versions[subject]
		if v < 1 || v > len(ids) {
			notFound(w, 40402)
			return
		}
		id := ids[v-1]
		writeJSON(w, http.StatusOK, sr.SubjectSchema{Subject: subject, Version: v, ID: id, Schema: f.schemas[id]})
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.m.Lock()
		defer f.m.Unlock()
		f.requests++
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f, srv.URL
}

// digestForFake is the fake registry's identity: the exact tuple, unhashed.
func digestForFake(s sr.Schema) []byte {
	b, _ := json.Marshal([]any{s.Type, s.References, s.Schema, s.SchemaMetadata, s.SchemaRuleSet})
	return b
}

func (f *fakeRegistry) resetRequests() int {
	f.m.Lock()
	defer f.m.Unlock()
	n := f.requests
	f.requests = 0
	return n
}

func newFakeClient(t *testing.T) (*Client, *fakeRegistry) {
	t.Helper()
	fake, url := newFakeRegistry(t)
	c, err := NewClient(log.Nop(), sr.URLs(url))
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

const orderSchema = `{"type":"record","name":"Order","fields":[{"name":"id","type":"string"},{"name":"amount_cents","type":"long"}]}`

// TestClient_CreateSchema_FingerprintCollision_SameSubject is the hermetic
// regression test for GHSA-95wx-33xh-gpvw. Before the fix, the second call hit
// subjectFingerprintCache, never contacted the registry and returned the first
// schema's ID and body.
func TestClient_CreateSchema_FingerprintCollision_SameSubject(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c, fake := newFakeClient(t)

	first, err := c.CreateSchema(ctx, "orders-value", sr.Schema{Schema: orderSchema})
	is.NoErr(err)

	forged := forgeCollidingAvroSchema(t, []byte(orderSchema), "Order")
	is.True(!bytes.Equal(forged, []byte(orderSchema)))
	is.Equal(rabin.Bytes(forged), rabin.Bytes([]byte(orderSchema))) // the forgery

	fake.resetRequests()
	second, err := c.CreateSchema(ctx, "orders-value", sr.Schema{Schema: string(forged)})
	is.NoErr(err)
	is.True(fake.resetRequests() > 0) // not served from the cache
	is.True(second.ID != first.ID)
	is.Equal(second.Schema.Schema, string(forged))

	// Byte-identical re-registration is still a cache hit.
	again, err := c.CreateSchema(ctx, "orders-value", sr.Schema{Schema: string(forged)})
	is.NoErr(err)
	is.Equal(again.ID, second.ID)
	is.Equal(fake.resetRequests(), 0)
}

func TestClient_CreateSchema_CacheKeyCoversTypeAndReferences(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c, fake := newFakeClient(t)

	avro, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, Type: sr.TypeAvro})
	is.NoErr(err)

	// Same subject and bytes, different type: must reach the registry and
	// return the JSON schema, not the cached Avro one.
	fake.resetRequests()
	js, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, Type: sr.TypeJSON})
	is.NoErr(err)
	is.True(fake.resetRequests() > 0)
	is.Equal(js.Schema.Type, sr.TypeJSON)
	is.True(js.ID != avro.ID)

	// Different references: also a miss.
	r1 := sr.SchemaReference{Name: "a", Subject: "sa", Version: 1}
	r2 := sr.SchemaReference{Name: "b", Subject: "sb", Version: 1}
	withRefs, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, References: []sr.SchemaReference{r1, r2}})
	is.NoErr(err)
	is.True(withRefs.ID != avro.ID)
	is.Equal(len(withRefs.Schema.References), 2)

	// Reference order is not meaningful: a hit.
	fake.resetRequests()
	reordered, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, References: []sr.SchemaReference{r2, r1}})
	is.NoErr(err)
	is.Equal(fake.resetRequests(), 0)
	is.Equal(reordered.ID, withRefs.ID)
}

func TestClient_CreateSchema_CacheKeyCoversMetadataAndRuleSet(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c, fake := newFakeClient(t)

	plain, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema})
	is.NoErr(err)

	// Empty metadata and rules are the same as unset: a cache hit.
	fake.resetRequests()
	empty, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, SchemaMetadata: &sr.SchemaMetadata{}, SchemaRuleSet: &sr.SchemaRuleSet{}})
	is.NoErr(err)
	is.Equal(fake.resetRequests(), 0)
	is.Equal(empty.ID, plain.ID)

	// Different metadata or rules: a miss that reaches the registry and
	// returns what was registered.
	meta := &sr.SchemaMetadata{Properties: map[string]string{"owner": "a", "tier": "1"}}
	withMeta, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, SchemaMetadata: meta})
	is.NoErr(err)
	is.True(fake.resetRequests() > 0)
	is.True(withMeta.ID != plain.ID)
	is.Equal(withMeta.Schema.SchemaMetadata.Properties["owner"], "a")

	rules := &sr.SchemaRuleSet{DomainRules: []sr.SchemaRule{{Name: "r", Expr: "true"}}}
	withRules, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, SchemaRuleSet: rules})
	is.NoErr(err)
	is.True(withRules.ID != plain.ID && withRules.ID != withMeta.ID)

	// Map order does not matter: a hit.
	fake.resetRequests()
	meta2 := &sr.SchemaMetadata{Properties: map[string]string{"tier": "1", "owner": "a"}}
	again, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema, SchemaMetadata: meta2})
	is.NoErr(err)
	is.Equal(fake.resetRequests(), 0)
	is.Equal(again.ID, withMeta.ID)
}

func TestClient_SchemaBySubjectVersion_DoesNotSeedCollidingDigest(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c, fake := newFakeClient(t)

	_, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema})
	is.NoErr(err)

	// A fresh client learns the schema through SchemaBySubjectVersion, which
	// also fills the digest cache.
	c2, err := NewClient(log.Nop(), sr.URLs(fake.url))
	is.NoErr(err)
	got, err := c2.SchemaBySubjectVersion(ctx, "s", 1)
	is.NoErr(err)
	is.Equal(got.Schema.Schema, orderSchema)

	fake.resetRequests()
	forged := forgeCollidingAvroSchema(t, []byte(orderSchema), "Order")
	second, err := c2.CreateSchema(ctx, "s", sr.Schema{Schema: string(forged)})
	is.NoErr(err)
	is.True(fake.resetRequests() > 0)
	is.Equal(second.Schema.Schema, string(forged))
}

// TestClient_ErrorsAreNotCached: a transient 503 must not stick.
func TestClient_ErrorsAreNotCached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c, fake := newFakeClient(t)

	fake.m.Lock()
	fake.failPOST = 1
	fake.m.Unlock()
	_, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema})
	is.True(err != nil)

	got, err := c.CreateSchema(ctx, "s", sr.Schema{Schema: orderSchema})
	is.NoErr(err)
	is.Equal(got.Schema.Schema, orderSchema)

	// Same for the lookups.
	_, err = c.SchemaByID(ctx, 99)
	is.True(err != nil)
	fake.m.Lock()
	fake.schemas[99] = sr.Schema{Schema: `"string"`}
	fake.m.Unlock()
	s, err := c.SchemaByID(ctx, 99)
	is.NoErr(err)
	is.Equal(s.Schema, `"string"`)

	_, err = c.SchemaBySubjectVersion(ctx, "later", 1)
	is.True(err != nil)
	fake.m.Lock()
	fake.versions["later"] = []int{99}
	fake.m.Unlock()
	ss, err := c.SchemaBySubjectVersion(ctx, "later", 1)
	is.NoErr(err)
	is.Equal(ss.ID, 99)
}

// builtinRegistryIsPatched probes the builtin registry linked into this test
// binary: v0.2.6 gives identical text registered as two different types the
// same ID; v0.2.7 and later (SHA-256 over type, references and text) does not.
// A behavioral probe is used because go test binaries carry no module
// version information to inspect.
func builtinRegistryIsPatched(t *testing.T) bool {
	t.Helper()
	ctx := context.Background()
	cl, err := sr.NewClient(sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := cl.CreateSchema(ctx, "probe-a", sr.Schema{Schema: `"string"`, Type: sr.TypeAvro})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cl.CreateSchema(ctx, "probe-b", sr.Schema{Schema: `"string"`, Type: sr.TypeJSON})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID != b.ID
}

// TestClient_CreateSchema_FingerprintCollision_BuiltinRegistry is the
// integration variant against the real builtin registry. It only passes once
// go.mod requires conduit-schema-registry v0.2.7 or later, because v0.2.6 has
// the same-class bug server side (GHSA-pfr7-p89q-w2jp); it is skipped until then.
func TestClient_CreateSchema_FingerprintCollision_BuiltinRegistry(t *testing.T) {
	if !builtinRegistryIsPatched(t) {
		t.Skip("needs github.com/conduitio/conduit-schema-registry >= v0.2.7 (GHSA-pfr7-p89q-w2jp)")
	}
	is := is.New(t)
	ctx := context.Background()

	registryURL := schemaregistrytest.TestSchemaRegistryURL(t)
	c, err := NewClient(log.Nop(), sr.URLs(registryURL))
	is.NoErr(err)

	first, err := c.CreateSchema(ctx, "orders-value", sr.Schema{Schema: orderSchema, Type: sr.TypeAvro})
	is.NoErr(err)
	is.Equal(first.Schema.Schema, orderSchema)

	forged := forgeCollidingAvroSchema(t, []byte(orderSchema), "Order")
	second, err := c.CreateSchema(ctx, "orders-value", sr.Schema{Schema: string(forged), Type: sr.TypeAvro})
	is.NoErr(err)
	is.Equal(second.Schema.Schema, string(forged))
	is.True(second.ID != first.ID)

	rawClient, err := sr.NewClient(sr.URLs(registryURL))
	is.NoErr(err)
	versions, err := rawClient.SubjectVersions(ctx, "orders-value")
	is.NoErr(err)
	is.Equal(len(versions), 2)
}
