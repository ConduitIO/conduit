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

package connector

import (
	"context"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
)

type cleanableDispenser struct {
	target  Dispenser
	cleanup func()
}

func (c *cleanableDispenser) DispenseSpecifier() (SpecifierPlugin, error) {
	return c.target.DispenseSpecifier()
}

func (c *cleanableDispenser) DispenseSource() (SourcePlugin, error) {
	plugin, err := c.target.DispenseSource()
	if err != nil {
		return nil, err
	}

	return &cleanableSourcePlugin{
		SourcePlugin: plugin,
		cleanup:      c.cleanup,
	}, nil
}

func (c *cleanableDispenser) DispenseDestination() (DestinationPlugin, error) {
	plugin, err := c.target.DispenseDestination()
	if err != nil {
		return nil, err
	}

	return &cleanableDestinationPlugin{
		DestinationPlugin: plugin,
		cleanup:           c.cleanup,
	}, nil
}

type cleanableSourcePlugin struct {
	SourcePlugin
	cleanup func()
}

func (c *cleanableSourcePlugin) Teardown(ctx context.Context, req pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	defer c.cleanup()

	return c.SourcePlugin.Teardown(ctx, req)
}

type cleanableDestinationPlugin struct {
	DestinationPlugin
	cleanup func()
}

func (c *cleanableDestinationPlugin) Teardown(ctx context.Context, req pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	defer c.cleanup()

	return c.DestinationPlugin.Teardown(ctx, req)
}
