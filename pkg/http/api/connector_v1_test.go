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

package api

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	apimock "github.com/conduitio/conduit/pkg/http/api/mock"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/inspector"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConnectorAPIv1_ListConnectors(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	source := newTestSource()
	destination := newTestDestination()
	destination.State = connector.DestinationState{
		Positions: map[string]opencdc.Position{source.ID: []byte("irrelevant")},
	}

	csMock.EXPECT().
		List(ctx).
		Return(map[string]*connector.Instance{source.ID: source, destination.ID: destination}).
		Times(1)

	now := time.Now()
	want := &apiv1.ListConnectorsResponse{
		Connectors: []*apiv1.Connector{
			{
				Id: source.ID,
				State: &apiv1.Connector_SourceState_{
					SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
				},
				Config: &apiv1.Connector_Config{
					Name:     source.Config.Name,
					Settings: source.Config.Settings,
				},
				Type:         apiv1.Connector_Type(source.Type),
				Plugin:       source.Plugin,
				PipelineId:   source.PipelineID,
				ProcessorIds: source.ProcessorIDs,
				CreatedAt:    timestamppb.New(now),
				UpdatedAt:    timestamppb.New(now),
			},

			{
				Id: destination.ID,
				State: &apiv1.Connector_DestinationState_{
					DestinationState: &apiv1.Connector_DestinationState{
						Positions: map[string][]byte{source.ID: []byte("irrelevant")},
					},
				},
				Config: &apiv1.Connector_Config{
					Name:     destination.Config.Name,
					Settings: destination.Config.Settings,
				},
				Type:         apiv1.Connector_Type(destination.Type),
				Plugin:       destination.Plugin,
				PipelineId:   destination.PipelineID,
				ProcessorIds: destination.ProcessorIDs,
				CreatedAt:    timestamppb.New(now),
				UpdatedAt:    timestamppb.New(now),
			},
		},
	}

	got, err := api.ListConnectors(
		ctx,
		&apiv1.ListConnectorsRequest{},
	)
	is.NoErr(err)

	// copy expected times
	for i, conn := range got.Connectors {
		conn.CreatedAt = want.Connectors[i].CreatedAt
		conn.UpdatedAt = want.Connectors[i].UpdatedAt
	}

	sortConnectors(want.Connectors)
	sortConnectors(got.Connectors)
	is.Equal(want, got)
}

func TestConnectorAPIv1_ListConnectorsByPipeline(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	source := newTestSource()
	destination := newTestDestination()

	csMock.EXPECT().
		List(ctx).
		Return(map[string]*connector.Instance{source.ID: source, destination.ID: destination}).
		Times(1)

	now := time.Now()
	want := &apiv1.ListConnectorsResponse{
		Connectors: []*apiv1.Connector{
			{
				Id: source.ID,
				State: &apiv1.Connector_SourceState_{
					SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
				},
				Config: &apiv1.Connector_Config{
					Name:     source.Config.Name,
					Settings: source.Config.Settings,
				},
				Type:         apiv1.Connector_Type(source.Type),
				Plugin:       source.Plugin,
				PipelineId:   source.PipelineID,
				ProcessorIds: source.ProcessorIDs,
				CreatedAt:    timestamppb.New(now),
				UpdatedAt:    timestamppb.New(now),
			},
		},
	}

	got, err := api.ListConnectors(
		ctx,
		&apiv1.ListConnectorsRequest{PipelineId: source.PipelineID},
	)
	is.NoErr(err)

	// copy expected time
	for i, conn := range got.Connectors {
		conn.CreatedAt = want.Connectors[i].CreatedAt
		conn.UpdatedAt = want.Connectors[i].UpdatedAt
	}

	is.Equal(want, got)
}

func TestConnectorAPIv1_CreateConnector(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	source := newTestSource()

	csMock.EXPECT().Create(ctx, source.Type, source.Plugin, source.PipelineID, source.Config).Return(source, nil).Times(1)

	now := time.Now()
	want := &apiv1.CreateConnectorResponse{Connector: &apiv1.Connector{
		Id: source.ID,
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     source.Config.Name,
			Settings: source.Config.Settings,
		},
		Type:         apiv1.Connector_Type(source.Type),
		Plugin:       source.Plugin,
		PipelineId:   source.PipelineID,
		ProcessorIds: source.ProcessorIDs,
		UpdatedAt:    timestamppb.New(now),
		CreatedAt:    timestamppb.New(now),
	}}

	got, err := api.CreateConnector(
		ctx,
		&apiv1.CreateConnectorRequest{
			Type:       want.Connector.Type,
			Plugin:     want.Connector.Plugin,
			PipelineId: want.Connector.PipelineId,
			Config:     want.Connector.Config,
		},
	)

	is.NoErr(err)

	// copy expected time
	got.Connector.CreatedAt = want.Connector.CreatedAt
	got.Connector.UpdatedAt = want.Connector.UpdatedAt

	is.Equal(want, got)
}

func TestConnectorAPIv1_InspectConnector_SendRecord(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	id := uuid.NewString()
	rec := generateTestRecord()
	recProto := &opencdcv1.Record{}
	err := rec.ToProto(recProto)
	is.NoErr(err)

	ins := inspector.New(log.Nop(), 10)
	session := ins.NewSession(ctx, "test-id")

	csMock.EXPECT().
		Inspect(ctx, id).
		Return(session, nil).
		Times(1)

	inspectChan := make(chan struct{})
	inspectServer := apimock.NewConnectorService_InspectConnectorServer(ctrl)
	inspectServer.EXPECT().
		Send(&apiv1.InspectConnectorResponse{Record: recProto}).
		DoAndReturn(func(_ *apiv1.InspectConnectorResponse) error {
			close(inspectChan)
			return nil
		})
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()

	go func() {
		_ = api.InspectConnector(
			&apiv1.InspectConnectorRequest{Id: id},
			inspectServer,
		)
	}()
	ins.Send(ctx, []opencdc.Record{rec})

	_, ok, err := cchan.ChanOut[struct{}](inspectChan).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(!ok)
}

func TestConnectorAPIv1_InspectConnector_SendErr(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()

	ins := inspector.New(log.Nop(), 10)
	session := ins.NewSession(ctx, "test-id")

	csMock.EXPECT().
		Inspect(ctx, id).
		Return(session, nil).
		Times(1)

	inspectServer := apimock.NewConnectorService_InspectConnectorServer(ctrl)
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()
	errSend := cerrors.New("I'm sorry, but no.")
	inspectServer.EXPECT().Send(gomock.Any()).Return(errSend)

	errC := make(chan error)
	go func() {
		err := api.InspectConnector(
			&apiv1.InspectConnectorRequest{Id: id},
			inspectServer,
		)
		errC <- err
	}()
	ins.Send(ctx, []opencdc.Record{generateTestRecord()})

	err, b, err2 := cchan.ChanOut[error](errC).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.NoErr(err2)
	is.True(b)                        // expected to receive an error
	is.True(cerrors.Is(err, errSend)) // expected 'I'm sorry, but no.' error"
}

func TestConnectorAPIv1_InspectConnector_Err(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()
	err := cerrors.New("not found, sorry")

	csMock.EXPECT().
		Inspect(ctx, gomock.Any()).
		Return(nil, err).
		Times(1)

	inspectServer := apimock.NewConnectorService_InspectConnectorServer(ctrl)
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()

	errAPI := api.InspectConnector(
		&apiv1.InspectConnectorRequest{Id: id},
		inspectServer,
	)
	is.True(errAPI != nil)
	is.Equal(
		"rpc error: code = Internal desc = failed to inspect connector: not found, sorry",
		errAPI.Error(),
	)
}

func generateTestRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("test-position"),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{"metadata-key": "metadata-value"},
		Key:       opencdc.RawData("test-key"),
		Payload: opencdc.Change{
			After: opencdc.RawData("test-payload"),
		},
	}
}

func TestConnectorAPIv1_GetConnector(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	source := newTestSource()

	csMock.EXPECT().Get(ctx, source.ID).Return(source, nil).Times(1)

	now := time.Now()
	want := &apiv1.GetConnectorResponse{Connector: &apiv1.Connector{
		Id: source.ID,
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     source.Config.Name,
			Settings: source.Config.Settings,
		},
		Type:         apiv1.Connector_Type(source.Type),
		Plugin:       source.Plugin,
		PipelineId:   source.PipelineID,
		ProcessorIds: source.ProcessorIDs,
		UpdatedAt:    timestamppb.New(now),
		CreatedAt:    timestamppb.New(now),
	}}

	got, err := api.GetConnector(
		ctx,
		&apiv1.GetConnectorRequest{
			Id: want.Connector.Id,
		},
	)

	is.NoErr(err)

	// copy expected time
	got.Connector.CreatedAt = want.Connector.CreatedAt
	got.Connector.UpdatedAt = want.Connector.UpdatedAt

	is.Equal(want, got)
}

func TestConnectorAPIv1_UpdateConnector(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	before := newTestSource()
	after := newTestSource()
	after.ID = before.ID
	after.Plugin = "test-plugin"
	after.Config = connector.Config{
		Name:     "A source connector",
		Settings: map[string]string{"path": "path/to"},
	}
	after.State = connector.SourceState{Position: []byte("irrelevant")}

	csMock.EXPECT().Update(ctx, before.ID, after.Plugin, after.Config).Return(after, nil).Times(1)

	now := time.Now()
	want := &apiv1.UpdateConnectorResponse{Connector: &apiv1.Connector{
		Id: after.ID,
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     after.Config.Name,
			Settings: after.Config.Settings,
		},
		Type:         apiv1.Connector_Type(after.Type),
		Plugin:       after.Plugin,
		PipelineId:   after.PipelineID,
		ProcessorIds: after.ProcessorIDs,
		CreatedAt:    timestamppb.New(now),
		UpdatedAt:    timestamppb.New(now),
	}}

	got, err := api.UpdateConnector(
		ctx,
		&apiv1.UpdateConnectorRequest{
			Id:     want.Connector.Id,
			Config: want.Connector.Config,
			Plugin: want.Connector.Plugin,
		},
	)
	is.NoErr(err)

	// copy expected time
	got.Connector.CreatedAt = want.Connector.CreatedAt
	got.Connector.UpdatedAt = want.Connector.UpdatedAt

	is.Equal(want, got)
}

func TestConnectorAPIv1_DeleteConnector(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	id := uuid.NewString()

	csMock.EXPECT().Delete(ctx, id).Return(nil).Times(1)

	want := &apiv1.DeleteConnectorResponse{}

	got, err := api.DeleteConnector(
		ctx,
		&apiv1.DeleteConnectorRequest{
			Id: id,
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestConnectorAPIv1_ValidateConnector(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	config := connector.Config{
		Name:     "A source connector",
		Settings: map[string]string{"path": "path/to"},
	}
	ctype := connector.TypeSource

	csMock.EXPECT().Validate(ctx, ctype, "builtin:file", config).Return(nil).Times(1)

	want := &apiv1.ValidateConnectorResponse{}

	got, err := api.ValidateConnector(
		ctx,
		&apiv1.ValidateConnectorRequest{
			Type:   apiv1.Connector_Type(ctype),
			Plugin: "builtin:file",
			Config: &apiv1.Connector_Config{
				Name:     config.Name,
				Settings: config.Settings,
			},
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestConnectorAPIv1_ValidateConnectorError(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock, nil)

	config := connector.Config{
		Name:     "A source connector",
		Settings: map[string]string{"path": "path/to"},
	}
	ctype := connector.TypeSource
	err := cerrors.New("validation error")

	csMock.EXPECT().Validate(ctx, ctype, "builtin:file", config).Return(err).Times(1)

	_, err = api.ValidateConnector(
		ctx,
		&apiv1.ValidateConnectorRequest{
			Type:   apiv1.Connector_Type(ctype),
			Plugin: "builtin:file",
			Config: &apiv1.Connector_Config{
				Name:     config.Name,
				Settings: config.Settings,
			},
		},
	)

	is.True(err != nil)
}

func TestConnectorAPIv1_ListConnectorPluginsByName(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cpoMock := apimock.NewConnectorPluginOrchestrator(ctrl)
	api := NewConnectorAPIv1(nil, cpoMock)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]pconnector.Specification)
	pls := make([]pconnector.Specification, 0)

	for _, name := range names {
		ps := pconnector.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			SourceParams: map[string]config.Parameter{
				"param": {
					Type: config.ParameterTypeString,
					Validations: []config.Validation{
						config.ValidationRequired{},
					},
				},
			},
			DestinationParams: map[string]config.Parameter{},
		}
		pls = append(pls, ps)
		plsMap[name] = ps
	}

	cpoMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	want := &apiv1.ListConnectorPluginsResponse{
		Plugins: []*apiv1.ConnectorPluginSpecifications{
			toproto.ConnectorPluginSpecifications(pls[1].Name, pls[1]),
			toproto.ConnectorPluginSpecifications(pls[2].Name, pls[2]),
		},
	}

	got, err := api.ListConnectorPlugins(
		ctx,
		&apiv1.ListConnectorPluginsRequest{Name: "want-.*"},
	)

	is.NoErr(err)

	sortPlugins := func(p []*apiv1.ConnectorPluginSpecifications) {
		sort.Slice(p, func(i, j int) bool {
			return p[i].Name < p[j].Name
		})
	}

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	is.Equal(want, got)
}

func sortConnectors(c []*apiv1.Connector) {
	sort.Slice(c, func(i, j int) bool {
		return c[i].Id < c[j].Id
	})
}

func newTestSource() *connector.Instance {
	source := &connector.Instance{
		ID:         uuid.NewString(),
		Plugin:     "path/to/plugin",
		PipelineID: uuid.NewString(),
		Config: connector.Config{
			Name:     "A source connector",
			Settings: map[string]string{"path": "path/to"},
		},
		Type:  connector.TypeSource,
		State: connector.SourceState{Position: []byte("irrelevant")},
	}
	return source
}

func newTestDestination() *connector.Instance {
	destination := &connector.Instance{
		ID:         uuid.NewString(),
		Plugin:     "path/to/plugin",
		PipelineID: uuid.NewString(),
		Config: connector.Config{
			Name:     "A destination connector",
			Settings: map[string]string{"path": "path/to"},
		},
		Type: connector.TypeDestination,
	}
	return destination
}
