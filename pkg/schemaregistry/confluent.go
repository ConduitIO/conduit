// Copyright © 2024 Meroxa, Inc.
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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

// ConfluentService interacts with the Confluent Schema Registry using Apicurio as a backend
type ConfluentService struct {
	client          *Client
	logger          log.CtxLogger
	connString      string
	healthCheckPath string
}

func NewConfluentService(ctx context.Context, l log.CtxLogger, connString, healthCheckPath string) *ConfluentService {
	client, err := NewClient(l, sr.URLs(connString))
	if err != nil {
		l.Err(ctx, err).Msg("failed to create confluent service client")
	}
	return &ConfluentService{
		client:          client,
		logger:          l,
		connString:      connString,
		healthCheckPath: healthCheckPath,
	}
}

func (c *ConfluentService) Create(ctx context.Context, name string, bytes []byte) (schema.Instance, error) {
	ss, err := c.client.CreateSchema(ctx, name, sr.Schema{
		Schema: string(bytes),
		Type:   sr.TypeAvro,
	})
	if err != nil {
		return schema.Instance{}, err
	}

	return schema.Instance{
		ID:      strconv.Itoa(ss.ID),
		Name:    name,
		Version: 0,
		Type:    schema.TypeAvro,
		Bytes:   []byte(ss.Schema.Schema),
	}, nil
}

func (c *ConfluentService) Get(ctx context.Context, id string) (schema.Instance, error) {
	schemaID, err := strconv.Atoi(id)
	if err != nil {
		c.logger.Err(ctx, err).Msg(fmt.Sprintf("invalid schema id: %s", id))
		return schema.Instance{}, err
	}
	s, err := c.client.SchemaByID(ctx, schemaID)
	if err != nil {
		c.logger.Err(ctx, err).Msg(fmt.Sprintf("failed to get schema by id: %s", id))
		return schema.Instance{}, err
	}

	return schema.Instance{
		ID:    id,
		Bytes: []byte(s.Schema),
	}, nil
}

func (c *ConfluentService) getHealthCheckURL() (string, error) {
	baseURL, err := url.Parse(c.connString)
	if err != nil {
		return "", err
	}
	path := strings.TrimLeft(c.healthCheckPath, "/")
	baseURL.Path = path
	return baseURL.String(), nil
}

func (c *ConfluentService) Check(ctx context.Context) error {
	url, err := c.getHealthCheckURL()
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.logger.Err(ctx, err).Msg("couldn't create http request for schema registry")
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.Err(ctx, err).Msg("couldn't connect with the schema registry")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Err(ctx, err).Msg(fmt.Sprintf("schema registry healthcheck failed with status code %d", resp.StatusCode))
		return err
	}

	return nil
}
