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

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/record"
	apimock "github.com/conduitio/conduit/pkg/web/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestConnectorAPIv1_ListConnectors(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)
	connBuilder := connmock.Builder{Ctrl: ctrl}
	source := newTestSource(connBuilder)
	destination := newTestDestination(connBuilder)
	destination.EXPECT().State().Return(connector.DestinationState{Positions: map[string]record.Position{source.ID(): []byte("irrelevant")}})

	csMock.EXPECT().
		List(ctx).
		Return(map[string]connector.Connector{source.ID(): source, destination.ID(): destination}).
		Times(1)

	want := &apiv1.ListConnectorsResponse{
		Connectors: []*apiv1.Connector{
			{
				Id: source.ID(),
				State: &apiv1.Connector_SourceState_{
					SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
				},
				Config: &apiv1.Connector_Config{
					Name:     source.Config().Name,
					Settings: source.Config().Settings,
				},
				Type:         apiv1.Connector_Type(source.Type()),
				Plugin:       source.Config().Plugin,
				PipelineId:   source.Config().PipelineID,
				ProcessorIds: source.Config().ProcessorIDs,
			},

			{
				Id: destination.ID(),
				State: &apiv1.Connector_DestinationState_{
					DestinationState: &apiv1.Connector_DestinationState{
						Positions: map[string][]byte{source.ID(): []byte("irrelevant")},
					},
				},
				Config: &apiv1.Connector_Config{
					Name:     destination.Config().Name,
					Settings: destination.Config().Settings,
				},
				Type:         apiv1.Connector_Type(destination.Type()),
				Plugin:       destination.Config().Plugin,
				PipelineId:   destination.Config().PipelineID,
				ProcessorIds: destination.Config().ProcessorIDs,
			},
		},
	}

	got, err := api.ListConnectors(
		ctx,
		&apiv1.ListConnectorsRequest{},
	)
	assert.Ok(t, err)

	sortConnectors(want.Connectors)
	sortConnectors(got.Connectors)
	assert.Equal(t, want, got)
}

func TestConnectorAPIv1_ListConnectorsByPipeline(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)
	connBuilder := connmock.Builder{Ctrl: ctrl}
	source := newTestSource(connBuilder)
	destination := newTestDestination(connBuilder)

	csMock.EXPECT().
		List(ctx).
		Return(map[string]connector.Connector{source.ID(): source, destination.ID(): destination}).
		Times(1)

	want := &apiv1.ListConnectorsResponse{
		Connectors: []*apiv1.Connector{
			{
				Id: source.ID(),
				State: &apiv1.Connector_SourceState_{
					SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
				},
				Config: &apiv1.Connector_Config{
					Name:     source.Config().Name,
					Settings: source.Config().Settings,
				},
				Type:         apiv1.Connector_Type(source.Type()),
				Plugin:       source.Config().Plugin,
				PipelineId:   source.Config().PipelineID,
				ProcessorIds: source.Config().ProcessorIDs,
			},
		},
	}

	got, err := api.ListConnectors(
		ctx,
		&apiv1.ListConnectorsRequest{PipelineId: source.Config().PipelineID},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConnectorAPIv1_CreateConnector(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)
	connBuilder := connmock.Builder{Ctrl: ctrl}
	source := newTestSource(connBuilder)

	csMock.EXPECT().Create(ctx, source.Type(), source.Config()).Return(source, nil).Times(1)

	want := &apiv1.CreateConnectorResponse{Connector: &apiv1.Connector{
		Id: source.ID(),
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     source.Config().Name,
			Settings: source.Config().Settings,
		},
		Type:         apiv1.Connector_Type(source.Type()),
		Plugin:       source.Config().Plugin,
		PipelineId:   source.Config().PipelineID,
		ProcessorIds: source.Config().ProcessorIDs,
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

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConnectorAPIv1_GetConnector(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)
	connBuilder := connmock.Builder{Ctrl: ctrl}
	source := newTestSource(connBuilder)

	csMock.EXPECT().Get(ctx, source.ID()).Return(source, nil).Times(1)

	want := &apiv1.GetConnectorResponse{Connector: &apiv1.Connector{
		Id: source.ID(),
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     source.Config().Name,
			Settings: source.Config().Settings,
		},
		Type:         apiv1.Connector_Type(source.Type()),
		Plugin:       source.Config().Plugin,
		PipelineId:   source.Config().PipelineID,
		ProcessorIds: source.Config().ProcessorIDs,
	}}

	got, err := api.GetConnector(
		ctx,
		&apiv1.GetConnectorRequest{
			Id: want.Connector.Id,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConnectorAPIv1_UpdateConnector(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)
	connBuilder := connmock.Builder{Ctrl: ctrl}
	oldConfig := connector.Config{
		Name:       "Old name",
		Settings:   map[string]string{"path": "old/path"},
		Plugin:     "path/to/plugin",
		PipelineID: uuid.NewString(),
	}
	newConfig := connector.Config{
		Name:       "A source connector",
		Settings:   map[string]string{"path": "path/to"},
		Plugin:     "path/to/plugin",
		PipelineID: oldConfig.PipelineID,
	}

	before := connBuilder.NewSourceMock(uuid.NewString(), oldConfig)
	after := connBuilder.NewSourceMock(before.ID(), newConfig)
	after.EXPECT().State().Return(connector.SourceState{Position: []byte("irrelevant")})

	csMock.EXPECT().Get(ctx, before.ID()).Return(before, nil).Times(1)
	csMock.EXPECT().Update(ctx, before.ID(), newConfig).Return(after, nil).Times(1)

	want := &apiv1.UpdateConnectorResponse{Connector: &apiv1.Connector{
		Id: after.ID(),
		State: &apiv1.Connector_SourceState_{
			SourceState: &apiv1.Connector_SourceState{Position: []byte("irrelevant")},
		},
		Config: &apiv1.Connector_Config{
			Name:     after.Config().Name,
			Settings: after.Config().Settings,
		},
		Type:         apiv1.Connector_Type(after.Type()),
		Plugin:       after.Config().Plugin,
		PipelineId:   after.Config().PipelineID,
		ProcessorIds: after.Config().ProcessorIDs,
	}}

	got, err := api.UpdateConnector(
		ctx,
		&apiv1.UpdateConnectorRequest{
			Id:     want.Connector.Id,
			Config: want.Connector.Config,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConnectorAPIv1_DeleteConnector(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	csMock := apimock.NewConnectorOrchestrator(ctrl)
	api := NewConnectorAPIv1(csMock)

	id := uuid.NewString()

	csMock.EXPECT().Delete(ctx, id).Return(nil).Times(1)

	want := &apiv1.DeleteConnectorResponse{}

	got, err := api.DeleteConnector(
		ctx,
		&apiv1.DeleteConnectorRequest{
			Id: id,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func sortConnectors(c []*apiv1.Connector) {
	sort.Slice(c, func(i, j int) bool {
		return c[i].Id < c[j].Id
	})
}

func newTestSource(connBuilder connmock.Builder) *connmock.Source {
	source := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{
		Name:       "A source connector",
		Settings:   map[string]string{"path": "path/to"},
		Plugin:     "path/to/plugin",
		PipelineID: uuid.NewString(),
	})
	source.EXPECT().State().Return(connector.SourceState{Position: []byte("irrelevant")})
	return source
}

func newTestDestination(connBuilder connmock.Builder) *connmock.Destination {
	destination := connBuilder.NewDestinationMock(uuid.NewString(), connector.Config{
		Name:       "A destination connector",
		Settings:   map[string]string{"path": "path/to"},
		Plugin:     "path/to/plugin",
		PipelineID: uuid.NewString(),
	})
	return destination
}
