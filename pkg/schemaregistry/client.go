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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/urlredact"
	"github.com/goccy/go-json"
	"github.com/twmb/franz-go/pkg/sr"
	"github.com/twmb/go-cache/cache"
)

// Client is a schema registry client that caches schemas. It is safe for
// concurrent use. The client caches schemas by ID, their subject/digest and
// subject/version. The digest is a SHA-256 over the subject, schema type,
// references and exact schema text. Load errors are not cached: a transient
// registry failure must not stick for the life of the process.
type Client struct {
	logger log.CtxLogger
	client sr.Client

	idCache             *cache.Cache[int, sr.Schema]
	subjectDigestCache  *cache.Cache[subjectDigest, sr.SubjectSchema]
	subjectVersionCache *cache.Cache[subjectVersion, sr.SubjectSchema]
}

type (
	subjectVersion string
	subjectDigest  string
)

func newSubjectVersion(subject string, version int) subjectVersion {
	return subjectVersion(fmt.Sprintf("%s:%d", subject, version))
}

// newSubjectDigest identifies a subject+schema pair by SHA-256 over the
// subject, the schema type, the canonicalised references, the exact schema
// text, and the schema metadata and rule set. It does not use a Rabin fingerprint: that is a linear 64-bit CRC, so an
// attacker who controls the schema text can forge a byte-different schema with
// the same fingerprint (same technique as GHSA-94xc-rjcp-83cr), which made
// CreateSchema return a different, pre-existing schema's ID and body for a
// genuinely new schema, silently, without ever contacting the registry. The
// fingerprint also ignored type and references, so the same text registered as
// a different type hit the same entry. Every variable-length field is
// length-prefixed so distinct inputs cannot produce the same hashed bytes, and
// references are sorted because their order is not meaningful.
func newSubjectDigest(subject string, s sr.Schema) subjectDigest {
	var b bytes.Buffer
	putInt := func(i int) {
		var v [8]byte
		// Two's-complement bit pattern of i on purpose: this is hashing framing,
		// not arithmetic, and bounds would change the keys.
		binary.BigEndian.PutUint64(v[:], uint64(int64(i))) //nolint:gosec // G115: intentional, see above
		b.Write(v[:])
	}
	putStr := func(s string) {
		putInt(len(s))
		b.WriteString(s)
	}

	putStr("conduit/schemaregistry/client-cache/v1")
	putStr(subject)
	putInt(int(s.Type))

	refs := slices.Clone(s.References)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		if refs[i].Subject != refs[j].Subject {
			return refs[i].Subject < refs[j].Subject
		}
		return refs[i].Version < refs[j].Version
	})
	putInt(len(refs))
	for _, r := range refs {
		putStr(r.Name)
		putStr(r.Subject)
		putInt(r.Version)
	}
	putStr(s.Schema)
	putStr(string(canonicalJSON(s.SchemaMetadata)))
	putStr(string(canonicalJSON(s.SchemaRuleSet)))

	return subjectDigest(fmt.Sprintf("%x", sha256.Sum256(b.Bytes())))
}

// canonicalJSON returns a deterministic encoding of an optional struct: the
// standard library sorts map keys, and nil and an empty struct both encode as
// nothing, so leaving metadata or rules unset keeps a schema's identity. It
// matches the encoding used by conduit-schema-registry's digest.
func canonicalJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	return raw
}

var _ RegistryWithCheck = (*Client)(nil)

// NewClient creates a new client using the provided logger and schema registry
// client options.
func NewClient(logger log.CtxLogger, opts ...sr.ClientOpt) (*Client, error) {
	defaultOpts := []sr.ClientOpt{
		sr.UserAgent("conduit"),
		sr.URLs(), // disable default URL
	}

	client, err := sr.NewClient(append(defaultOpts, opts...)...)
	if err != nil {
		return nil, cerrors.Errorf("failed to create schema registry client: %w", urlredact.Error(err))
	}

	return &Client{
		logger: logger.WithComponent("schemaregistry.Client"),
		client: *client,

		idCache:             cache.New[int, sr.Schema](cache.MaxErrorAge(0)),
		subjectDigestCache:  cache.New[subjectDigest, sr.SubjectSchema](cache.MaxErrorAge(0)),
		subjectVersionCache: cache.New[subjectVersion, sr.SubjectSchema](cache.MaxErrorAge(0)),
	}, nil
}

// CreateSchema checks if the schema is already registered in the cache and
// returns the associated sr.SubjectSchema if it is found. Otherwise, the schema
// is sent to the schema registry and stored in the cache, if the registration
// was successful.
func (c *Client) CreateSchema(ctx context.Context, subject string, schema sr.Schema) (sr.SubjectSchema, error) {
	logEvent := c.logger.Trace(ctx).Str("operation", "CreateSchema").Str("subject", subject)

	sd := newSubjectDigest(subject, schema)
	ss, err, _ := c.subjectDigestCache.Get(sd, func() (sr.SubjectSchema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit

		// Check if the subject exists. Ignore the error as this is not critical
		// for creating a schema, we assume the subject doesn't exist in case of an error.
		versions, _ := c.client.SubjectVersions(sr.WithParams(ctx, sr.ShowDeleted), subject)
		subjectExists := len(versions) > 0

		ss, err := c.client.CreateSchema(ctx, subject, schema)
		if err != nil {
			return ss, cerrors.Errorf("failed to create schema with subject %q: %w", subject, urlredact.Error(err))
		}

		if !subjectExists {
			// if we created the schema we need to disable compatibility checks
			result := c.client.SetCompatibility(ctx, sr.SetCompatibility{Level: sr.CompatNone}, subject)
			for _, res := range result {
				if res.Err != nil {
					// only log error, don't return it
					c.logger.Warn(ctx).Err(urlredact.Error(res.Err)).Str("subject", subject).Msg("failed to set compatibility to none, might create issues if an incompatible schema change happens in the future")
				}
			}
		}
		c.idCache.Set(ss.ID, ss.Schema)
		c.subjectVersionCache.Set(newSubjectVersion(ss.Subject, ss.Version), ss)
		return ss, nil
	})
	if err != nil {
		return sr.SubjectSchema{}, err
	}
	logEvent.Msg("schema cache hit")
	return ss, nil
}

// SchemaByID checks if the schema is already registered in the cache and
// returns the associated sr.Schema if it is found. Otherwise, the schema is
// retrieved from the schema registry and stored in the cache.
// Note that the returned schema does not contain a subject and version, so the
// cache will not have an effect on methods that return a sr.SubjectSchema.
func (c *Client) SchemaByID(ctx context.Context, id int) (sr.Schema, error) {
	logEvent := c.logger.Trace(ctx).Str("operation", "SchemaByID").Int("id", id)

	s, err, _ := c.idCache.Get(id, func() (sr.Schema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit
		ss, err := c.client.SchemaByID(ctx, id)
		if err != nil {
			return sr.Schema{}, cerrors.Errorf("failed to get schema with ID %q: %w", id, urlredact.Error(err))
		}
		return ss, nil
	})
	if err != nil {
		return sr.Schema{}, err
	}
	logEvent.Msg("schema cache hit")
	return s, nil
}

// SchemaBySubjectVersion checks if the schema is already registered in the
// cache and returns the associated sr.SubjectSchema if it is found. Otherwise,
// the schema is retrieved from the schema registry and stored in the cache.
func (c *Client) SchemaBySubjectVersion(ctx context.Context, subject string, version int) (sr.SubjectSchema, error) {
	// TODO handle latest version separately, let caller define timeout after
	//  which the latest cached version should be downloaded again from upstream
	logEvent := c.logger.Trace(ctx).Str("operation", "SchemaBySubjectVersion").Str("subject", subject).Int("version", version)

	sv := newSubjectVersion(subject, version)
	ss, err, _ := c.subjectVersionCache.Get(sv, func() (sr.SubjectSchema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit
		ss, err := c.client.SchemaByVersion(ctx, subject, version)
		if err != nil {
			return ss, cerrors.Errorf("failed to get schema with subject %q and version %q: %w", subject, version, urlredact.Error(err))
		}
		c.idCache.Set(ss.ID, ss.Schema)
		c.subjectDigestCache.Set(newSubjectDigest(ss.Subject, ss.Schema), ss)
		return ss, nil
	})
	if err != nil {
		return sr.SubjectSchema{}, err
	}
	logEvent.Msg("schema cache hit")
	return ss, nil
}

// Check checks if the schema registry is reachable.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.client.Subjects(ctx) // just check if we can list subjects
	return urlredact.Error(err)
}
