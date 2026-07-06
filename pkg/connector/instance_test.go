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

package connector

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/matryer/is"
)

// fakePluginFetcher fulfills the PluginFetcher interface.
type fakePluginFetcher map[string]connectorPlugin.Dispenser

func (fpf fakePluginFetcher) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	plug, ok := fpf[name]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	return plug, nil
}

// fakeConnector fulfills the Connector interface, used to simulate an
// already-running connector on an Instance.
type fakeConnector struct{}

func (fakeConnector) OnDelete(context.Context) error { return nil }

// TestInstance_Connector_AlreadyRunning proves Instance.Connector still
// returns ErrConnectorRunning (the sentinel every errors.Is(err,
// ErrConnectorRunning) check throughout the codebase relies on) when a
// connector is already running, and that the error now also carries a
// machine-actionable ConduitError code + suggestion.
func TestInstance_Connector_AlreadyRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	i := &Instance{ID: "conn1", Type: TypeSource}
	i.Init(log.Nop(), nil)
	i.connector = fakeConnector{}

	got, err := i.Connector(ctx, fakePluginFetcher{})
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrConnectorRunning)) // sentinel still in the chain

	ce, ok := conduiterr.Get(err)
	is.True(ok) // also carries a machine-actionable ConduitError code
	is.Equal(ce.Code.Reason(), CodeConnectorRunning.Reason())
	is.True(ce.Suggestion != "") // with a suggested fix
}
