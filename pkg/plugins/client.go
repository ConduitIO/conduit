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

package plugins

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
)

// TODO this file should stay in conduit (it's not needed for developing a
//  plugin), everything else should be moved to the connector plugin SDK.

// NewClient creates a new plugin client. The provided context is used to kill
// the process (by calling os.Process.Kill) if the context becomes done before
// the plugin completes on its own. Path should point to the plugin executable.
func NewClient(ctx context.Context, logger zerolog.Logger, path string) *plugin.Client {
	cmd := createCommand(ctx, path)

	// NB: we give cmd a clean env here by setting Env to an empty slice
	cmd.Env = make([]string, 0)
	return plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: Handshake,
		Plugins:         PluginMap,
		Cmd:             cmd,
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		SyncStderr: logger,
		SyncStdout: logger,
		Logger: &hcLogger{
			logger: logger,
		},
	})
}

// DispenseSource will run the plugin executable if it's not already running and
// dispense the source plugin.
func DispenseSource(client *plugin.Client) (Source, error) {
	rpcClient, err := client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("source")
	if err != nil {
		return nil, err
	}

	source, ok := raw.(Source)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense source, got type: %T", raw)
	}

	return source, nil
}

// DispenseDestination will run the plugin executable if it's not already running and
// dispense the destination plugin.
func DispenseDestination(client *plugin.Client) (Destination, error) {
	rpcClient, err := client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("destination")
	if err != nil {
		return nil, err
	}

	destination, ok := raw.(Destination)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense destination, got type: %T", raw)
	}

	return destination, nil
}

// DispenseSpecifier will run the plugin executable and return the Specifier
// if any from that plugin.
func DispenseSpecifier(client *plugin.Client) (Specifier, error) {
	rpcClient, err := client.Client()
	if err != nil {
		return nil, cerrors.Errorf("failed to get client: %w", err)
	}

	raw, err := rpcClient.Dispense("specifier")
	if err != nil {
		return nil, cerrors.Errorf("failed to dispense spec: %w", err)
	}

	v, ok := raw.(Specifier)
	if !ok {
		return nil, cerrors.Errorf("does not implement Specifier: %T", raw)
	}
	return v, nil
}
