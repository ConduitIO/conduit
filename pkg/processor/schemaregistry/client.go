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

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/processor/schemaregistry/internal"
	"github.com/twmb/franz-go/pkg/sr"
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
	ss, ok := c.cache.GetBySubjectText(subject, schema.Schema)
	logEvent := c.logger.Trace(ctx).Str("subject", subject)
	if ok {
		logEvent.Msg("schema cache hit")
		return ss, nil
	}
	logEvent.Msg("schema cache miss")

	ss, err := c.client.CreateSchema(ctx, subject, schema)
	if err != nil {
		return sr.SubjectSchema{}, err
	}

	c.cache.AddSubjectSchema(ss)
	return ss, nil
}

// SchemaByID checks if the schema is already registered in the cache and
// returns the associated sr.Schema if it is found. Otherwise, the schema is
// retrieved from the schema registry and stored in the cache.
// Note that the returned schema does not contain a subject and version, so the
// cache will not have an effect on methods that return a sr.SubjectSchema.
func (c *Client) SchemaByID(ctx context.Context, id int) (sr.Schema, error) {
	s, ok := c.cache.GetByID(id)
	logEvent := c.logger.Trace(ctx).Int("id", id)
	if ok {
		logEvent.Msg("schema cache hit")
		return s, nil
	}
	logEvent.Msg("schema cache miss")

	s, err := c.client.SchemaByID(ctx, id)
	if err != nil {
		return sr.Schema{}, err
	}

	c.cache.AddSchema(id, s)
	return s, nil
}

// SchemaBySubjectVersion checks if the schema is already registered in the
// cache and returns the associated sr.SubjectSchema if it is found. Otherwise,
// the schema is retrieved from the schema registry and stored in the cache.
func (c *Client) SchemaBySubjectVersion(ctx context.Context, subject string, version int) (sr.SubjectSchema, error) {
	// TODO handle latest version separately, let caller define timeout after
	//  which the latest cached version should be downloaded again from upstream
	ss, ok := c.cache.GetBySubjectVersion(subject, version)
	logEvent := c.logger.Trace(ctx).Str("subject", subject).Int("version", version)
	if ok {
		logEvent.Msg("schema cache hit")
		return ss, nil
	}
	logEvent.Msg("schema cache miss")

	ss, err := c.client.SchemaByVersion(ctx, subject, version, sr.HideDeleted)
	if err != nil {
		return sr.SubjectSchema{}, err
	}

	c.cache.AddSubjectSchema(ss)
	return ss, nil
}
