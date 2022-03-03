// Copyright © 2022 Meroxa, Inc.
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

package source

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/pg/source/cdc"
	"github.com/conduitio/conduit/pkg/plugins/pg/source/snapshot"
)

// requiredFields is a list of our plugin required config fields for validation
var requiredFields = []string{"url", "table"}

var _ Strategy = (*cdc.Iterator)(nil)
var _ Strategy = (*snapshot.Snapshotter)(nil)

// Source implements the new transition to the new plugin SDK for Postgres.
type Source struct {
	sdk.UnimplementedSource

	Iterator Strategy

	config map[string]string
}

func NewSource() sdk.Source {
	return &Source{}
}

func (s *Source) Configure(ctx context.Context, cfg map[string]string) error {
	err := validateConfig(cfg, requiredFields)
	if err != nil {
		return cerrors.Errorf("config failed validation: %w", err)
	}
	s.config = cfg
	return nil
}
func (s *Source) Open(ctx context.Context, pos sdk.Position) error {
	switch s.config["mode"] {
	// TODO add other modes
	default:
		var columns []string
		if colsRaw := s.config["columns"]; colsRaw != "" {
			columns = strings.Split(s.config["columns"], ",")
		}
		i, err := cdc.NewCDCIterator(ctx, cdc.Config{
			Position:        pos,
			URL:             s.config["url"],
			SlotName:        s.config["slot_name"],
			PublicationName: s.config["publication_name"],
			TableName:       s.config["table"],
			KeyColumnName:   s.config["key"],
			Columns:         columns,
		})
		if err != nil {
			return cerrors.Errorf("failed to open cdc connection: %w", err)
		}
		s.Iterator = i
	}
	return nil
}

func (s *Source) Read(ctx context.Context) (sdk.Record, error) {
	return s.Iterator.Next(ctx)
}

func (s *Source) Ack(context.Context, sdk.Position) error {
	return nil
}

func (s *Source) Teardown(context.Context) error {
	if s.Iterator == nil {
		return nil
	}
	return s.Iterator.Teardown()
}

// returns an error if the cfg passed does not have all of the keys in required
func validateConfig(cfg map[string]string, required []string) error {
	for _, k := range required {
		if _, ok := cfg[k]; !ok {
			return cerrors.Errorf("plugin config missing required field %s", k)
		}
	}
	return nil
}
