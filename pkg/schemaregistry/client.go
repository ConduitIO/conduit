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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry/internal"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

// Client is a schema registry client that caches schemas. It is safe for
// concurrent use.
type Client struct {
	logger log.CtxLogger
	client sr.Client

	cache internal.SchemaCache
}

// NewClient creates a new client using the provided logger and schema registry
// client options.
func NewClient(logger log.CtxLogger, opts ...sr.Opt) (*Client, error) {
	defaultOpts := []sr.Opt{
		sr.UserAgent("conduit"),
		sr.URLs(), // disable default URL
	}

	client, err := sr.NewClient(append(defaultOpts, opts...)...)
	if err != nil {
		return nil, err
	}

	return &Client{
		logger: logger,
		client: *client,
	}, nil
}

// CreateSchema checks if the schema is already registered in the cache and
// returns the associated sr.SubjectSchema if it is found. Otherwise, the schema
// is sent to the schema registry and stored in the cache, if the registration
// was successful.
func (c *Client) CreateSchema(ctx context.Context, subject string, schema sr.Schema) (sr.SubjectSchema, error) {
	logEvent := c.logger.Trace(ctx).Str("operation", "CreateSchema").Str("subject", subject)
	ss, err := c.cache.GetBySubjectText(subject, schema.Schema, func() (sr.SubjectSchema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit

		// Check if the subject exists. Ignore the error as this is not critical
		// for creating a schema, we assume the subject exists in case of an error.
		versions, _ := c.client.SubjectVersions(ctx, subject, sr.ShowDeleted)
		subjectExists := len(versions) > 0

		ss, err := c.client.CreateSchema(ctx, subject, schema)
		if err != nil {
			return ss, err
		}

		if !subjectExists {
			// if we are created the schema we need to disable compatibility checks
			c.client.SetCompatibilityLevel(ctx, sr.CompatNone, subject)
		}
		return ss, nil
	})
	if err != nil {
		return sr.SubjectSchema{}, cerrors.Errorf("failed to create schema with subject %q: %w", subject, err)
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
	s, err := c.cache.GetByID(id, func() (sr.Schema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit
		return c.client.SchemaByID(ctx, id)
	})
	if err != nil {
		return sr.Schema{}, cerrors.Errorf("failed to get schema with ID %q: %w", id, err)
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
	ss, err := c.cache.GetBySubjectVersion(subject, version, func() (sr.SubjectSchema, error) {
		logEvent.Msg("schema cache miss")
		logEvent = nil // disable output for hit
		return c.client.SchemaByVersion(ctx, subject, version, sr.HideDeleted)
	})
	if err != nil {
		return sr.SubjectSchema{}, cerrors.Errorf("failed to get schema with subject %q and version %q: %w", subject, version, err)
	}
	logEvent.Msg("schema cache hit")
	return ss, nil
}
