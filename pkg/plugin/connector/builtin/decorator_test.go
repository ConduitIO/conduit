// Copyright © 2026 Meroxa, Inc.
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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

type decoratorKey struct{}

var seenDecorated []any

type probeSourceConfig struct{ sdk.DefaultSourceMiddleware }

func (c probeSourceConfig) Validate(ctx context.Context) error {
	seenDecorated = append(seenDecorated, ctx.Value(decoratorKey{}))
	return c.DefaultSourceMiddleware.Validate(ctx)
}

type probeSource struct {
	sdk.UnimplementedSource
	cfg probeSourceConfig
}

func (s *probeSource) Config() sdk.SourceConfig { return &s.cfg }

func probeConnector(name string) sdk.Connector {
	return sdk.Connector{
		NewSpecification: func() pconnector.Specification {
			return pconnector.Specification{Name: name, Version: "v0.0.1", Author: "test", Summary: "probe"}
		},
		NewSource: func() sdk.Source { return sdk.SourceWithMiddleware(&probeSource{}) },
	}
}

// The decorator reaches calls into the connector it was registered for and no
// other.
func TestRegistry_ContextDecorator(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	connectors := map[string]sdk.Connector{
		"github.com/conduitio/conduit-connector-decorated": probeConnector("decorated"),
		"github.com/conduitio/conduit-connector-plain":     probeConnector("plain"),
	}
	r := NewRegistry(log.Nop(), connectors, nil,
		WithContextDecorator("decorated", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, decoratorKey{}, "engine-value")
		}))
	r.Init(ctx)

	configure := func(name string) any {
		seenDecorated = nil
		d, err := r.NewDispenser(log.Nop(), plugin.FullName("builtin:"+name), pconnector.PluginConfig{})
		is.NoErr(err)
		src, err := d.DispenseSource()
		is.NoErr(err)
		_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: map[string]string{}})
		is.NoErr(err)
		is.True(len(seenDecorated) > 0)
		return seenDecorated[len(seenDecorated)-1]
	}
	is.Equal(configure("decorated"), "engine-value")
	is.Equal(configure("plain"), nil)
}
