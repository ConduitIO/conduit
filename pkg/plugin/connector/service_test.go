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

package connector_test

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestService_Init(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	builtinReg := mock.NewBuiltinReg(ctrl)
	builtinReg.EXPECT().Init(ctx)

	standaloneReg := mock.NewStandaloneReg(ctrl)
	standaloneReg.EXPECT().Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

	underTest := connector.NewPluginService(
		log.Nop(),
		builtinReg,
		standaloneReg,
		mock.NewAuthManager(ctrl),
	)

	underTest.Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)
	is.NoErr(underTest.Check(ctx))
}

func TestService_NewDispenser(t *testing.T) {
	testCases := []struct {
		name    string
		plugin  string
		setup   func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg)
		wantErr bool
	}{
		{
			name:   "standalone plugin found",
			plugin: "standalone:foobar-connector",
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				standaloneReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("standalone:foobar-connector")),
						gomock.Any(),
					).Return(nil, nil)
			},
		},
		{
			name:    "standalone plugin not found",
			wantErr: true,
			plugin:  "standalone:foobar-connector",
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				standaloneReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("standalone:foobar-connector")),
						gomock.Any(),
					).Return(nil, plugin.ErrPluginNotFound)
			},
		},
		{
			name:   "builtin plugin found",
			plugin: "builtin:foobar-connector",
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				builtinReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("builtin:foobar-connector")),
						gomock.Any(),
					).Return(nil, nil)
			},
		},
		{
			name:    "builtin plugin not found",
			plugin:  "builtin:foobar-connector",
			wantErr: true,
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				builtinReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("builtin:foobar-connector")),
						gomock.Any(),
					).Return(nil, plugin.ErrPluginNotFound)
			},
		},
		{
			name:   "no plugin type, standalone is assumed",
			plugin: "foobar-connector",
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				standaloneReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("foobar-connector")),
						gomock.Any(),
					).Return(nil, nil)
			},
		},
		{
			name:   "plugin without type not found, fall back to built-in",
			plugin: "foobar-connector",
			setup: func(builtinReg *mock.BuiltinReg, standaloneReg *mock.StandaloneReg) {
				standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
				standaloneReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("foobar-connector")),
						gomock.Any(),
					).Return(nil, plugin.ErrPluginNotFound)

				builtinReg.EXPECT().
					NewDispenser(
						gomock.Any(),
						gomock.Eq(plugin.FullName("foobar-connector")),
						gomock.Any(),
					).Return(nil, nil)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			authManager := mock.NewAuthManager(ctrl)
			authManager.EXPECT().GenerateNew("foobar-connector-id")

			builtinReg := mock.NewBuiltinReg(ctrl)
			builtinReg.EXPECT().Init(ctx)

			standaloneReg := mock.NewStandaloneReg(ctrl)
			standaloneReg.EXPECT().Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

			if tc.setup != nil {
				tc.setup(builtinReg, standaloneReg)
			}

			underTest := connector.NewPluginService(
				log.Nop(),
				builtinReg,
				standaloneReg,
				authManager,
			)

			underTest.Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)
			_, err := underTest.NewDispenser(log.Nop(), tc.plugin, "foobar-connector-id")
			if tc.wantErr {
				is.True(cerrors.Is(err, plugin.ErrPluginNotFound))
			} else {
				is.NoErr(err)
			}
		})
	}
}

func TestService_NewDispenser_InvalidPluginPrefix(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	authManager := mock.NewAuthManager(ctrl)
	authManager.EXPECT().GenerateNew("foobar-connector-id")

	builtinReg := mock.NewBuiltinReg(ctrl)
	builtinReg.EXPECT().Init(ctx)

	standaloneReg := mock.NewStandaloneReg(ctrl)
	standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
	standaloneReg.EXPECT().Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

	underTest := connector.NewPluginService(
		log.Nop(),
		builtinReg,
		standaloneReg,
		authManager,
	)

	underTest.Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)
	_, err := underTest.NewDispenser(log.Nop(), "mistake:plugin-name", "foobar-connector-id")
	is.True(err != nil)
	is.Equal(`invalid plugin name prefix "mistake"`, err.Error())
}

func TestService_NewDispenser_Source_TokenHandling(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	conn := "builtin:foobar-connector"
	connID := "foobar-connector-id"
	token := "test-token"
	logger := log.Nop()

	mockSourcePlugin := mock.NewSourcePlugin(ctrl)
	mockSourcePlugin.EXPECT().Teardown(ctx, pconnector.SourceTeardownRequest{})

	mockDispenser := mock.NewDispenser(ctrl)
	mockDispenser.EXPECT().DispenseSource().Return(mockSourcePlugin, nil)

	builtinReg := mock.NewBuiltinReg(ctrl)
	builtinReg.EXPECT().Init(ctx)
	builtinReg.EXPECT().
		NewDispenser(
			gomock.Any(),
			plugin.FullName(conn),
			pconnector.PluginConfig{
				Token:       token,
				ConnectorID: connID,
				LogLevel:    logger.GetLevel().String(),
				Grpc: pconnector.GRPCConfig{
					MaxReceiveRecordSize: server.DefaultMaxReceiveRecordSize,
				},
			},
		).
		Return(mockDispenser, nil)

	standaloneReg := mock.NewStandaloneReg(ctrl)
	standaloneReg.EXPECT().Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)
	standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)

	authManager := mock.NewAuthManager(ctrl)
	authManager.EXPECT().GenerateNew(connID).Return(token)
	authManager.EXPECT().Deregister(token)

	underTest := connector.NewPluginService(logger, builtinReg, standaloneReg, authManager)
	underTest.Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

	dispenser, err := underTest.NewDispenser(logger, conn, connID)
	is.NoErr(err)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)
	_, err = source.Teardown(ctx, pconnector.SourceTeardownRequest{})
	is.NoErr(err)
}

func TestService_NewDispenser_Destination_TokenHandling(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	conn := "builtin:foobar-connector"
	connID := "foobar-connector-id"
	token := "test-token"
	logger := log.Nop()

	mockDestinationPlugin := mock.NewDestinationPlugin(ctrl)
	mockDestinationPlugin.EXPECT().Teardown(ctx, pconnector.DestinationTeardownRequest{})

	mockDispenser := mock.NewDispenser(ctrl)
	mockDispenser.EXPECT().DispenseDestination().Return(mockDestinationPlugin, nil)

	builtinReg := mock.NewBuiltinReg(ctrl)
	builtinReg.EXPECT().Init(ctx)
	builtinReg.EXPECT().
		NewDispenser(
			gomock.Any(),
			plugin.FullName(conn),
			pconnector.PluginConfig{
				Token:       token,
				ConnectorID: connID,
				LogLevel:    logger.GetLevel().String(),
				Grpc: pconnector.GRPCConfig{
					MaxReceiveRecordSize: server.DefaultMaxReceiveRecordSize,
				},
			},
		).
		Return(mockDispenser, nil)

	standaloneReg := mock.NewStandaloneReg(ctrl)
	standaloneReg.EXPECT().GetMaxReceiveRecordSize().Return(server.DefaultMaxReceiveRecordSize).Times(1)
	standaloneReg.EXPECT().Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

	authManager := mock.NewAuthManager(ctrl)
	authManager.EXPECT().GenerateNew(connID).Return(token)
	authManager.EXPECT().Deregister(token)

	underTest := connector.NewPluginService(logger, builtinReg, standaloneReg, authManager)
	underTest.Init(ctx, "test-conn-utils-address", server.DefaultMaxReceiveRecordSize)

	dispenser, err := underTest.NewDispenser(logger, conn, connID)
	is.NoErr(err)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)
	_, err = destination.Teardown(ctx, pconnector.DestinationTeardownRequest{})
	is.NoErr(err)
}
