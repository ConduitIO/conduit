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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/pg/source/cdc"
	"github.com/conduitio/conduit/pkg/plugins/pg/source/snapshot"
)

// Required is a list of our plugin required config fields for validation
var Required = []string{"url", "table"}

// Enforce that we fulfill V1Source
var _ sdk.Source = (*Source)(nil)
var _ Strategy = (*cdc.Iterator)(nil)
var _ Strategy = (*snapshot.Snapshotter)(nil)

// Source implements the new transition to the new plugin SDK for Postgres.
type Source struct {
	sdk.UnimplementedSource

	Iterator Strategy

	config map[string]string
}

// Configure validates a config map and returns an error if anything is invalid.
func (s *Source) Configure(ctx context.Context, cfg map[string]string) error {
	err := validateConfig(cfg, Required)
	if err != nil {
		return cerrors.Errorf("config failed validation: %w", err)
	}
	s.config = cfg
	return nil
}
func (s *Source) Open(ctx context.Context, pos sdk.Position) error {
	switch s.config["mode"] {
	case "cdc":
		i, err := cdc.NewCDCIterator(ctx, cdc.Config{})
		if err != nil {
			return cerrors.Errorf("failed to open cdc connection: %w", err)
		}
		s.Iterator = i
	case "":
		i, err := cdc.NewCDCIterator(ctx, cdc.Config{})
		if err != nil {
			return cerrors.Errorf("failed to open cdc connection: %w", err)
		}
		s.Iterator = i
	default:
		i, err := cdc.NewCDCIterator(ctx, cdc.Config{})
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
	return cerrors.ErrNotImpl
}

func (s *Source) Teardown(context.Context) error {
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
