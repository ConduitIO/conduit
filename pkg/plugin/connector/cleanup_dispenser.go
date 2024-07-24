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

// CleanupDispenser dispenses sources and destinations
// for which a cleanup function will be called after they are torn down.
type CleanupDispenser struct {
	Target  Dispenser
	Cleanup func()
}

func (c *CleanupDispenser) DispenseSpecifier() (SpecifierPlugin, error) {
	return c.Target.DispenseSpecifier()
}

func (c *CleanupDispenser) DispenseSource() (SourcePlugin, error) {
	plugin, err := c.Target.DispenseSource()
	if err != nil {
		return nil, err
	}

	return &cleanupSourcePlugin{
		SourcePlugin: plugin,
		cleanup:      c.Cleanup,
	}, nil
}

func (c *CleanupDispenser) DispenseDestination() (DestinationPlugin, error) {
	plugin, err := c.Target.DispenseDestination()
	if err != nil {
		return nil, err
	}

	return &cleanupDestinationPlugin{
		DestinationPlugin: plugin,
		cleanup:           c.Cleanup,
	}, nil
}

// cleanupSourcePlugin is a SourcePlugin that can run a cleanup function
// after its Teardown() method is called
type cleanupSourcePlugin struct {
	SourcePlugin
	cleanup func()
}

func (c *cleanupSourcePlugin) Teardown(ctx context.Context, req pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	defer c.cleanup()

	return c.SourcePlugin.Teardown(ctx, req)
}

// cleanupDestinationPlugin is a DestinationPlugin that can run a cleanup function
// after its Teardown() method is called
type cleanupDestinationPlugin struct {
	DestinationPlugin
	cleanup func()
}

func (c *cleanupDestinationPlugin) Teardown(ctx context.Context, req pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	defer c.cleanup()

	return c.DestinationPlugin.Teardown(ctx, req)
}
