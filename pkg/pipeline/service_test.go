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

package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// serviceTestSetup is a helper struct, which generates source and destination mocks.
// Another test (lifecycle_test.go) in this package uses similar methods, but with slightly different expected behavior.
// Hence, not to pollute the package namespace, we have this helper struct.
type serviceTestSetup struct {
	t *testing.T
}

func (s *serviceTestSetup) basicSourceMock(ctrl *gomock.Controller) *connmock.Source {
	source := connmock.NewSource(ctrl)
	source.EXPECT().ID().Return(uuid.NewString()).AnyTimes()
	source.EXPECT().Type().Return(connector.TypeSource).AnyTimes()
	source.EXPECT().Config().Return(connector.Config{}).AnyTimes()
	source.EXPECT().Open(gomock.Any()).AnyTimes()
	source.EXPECT().Read(gomock.Any()).AnyTimes()
	source.EXPECT().Ack(gomock.Any(), gomock.Any()).AnyTimes()
	source.EXPECT().Errors().AnyTimes()
	source.EXPECT().Teardown(gomock.Any()).AnyTimes()

	return source
}

func (s *serviceTestSetup) basicDestinationMock(ctrl *gomock.Controller) *connmock.Destination {
	destination := connmock.NewDestination(ctrl)
	destination.EXPECT().ID().Return(uuid.NewString()).AnyTimes()
	destination.EXPECT().Type().Return(connector.TypeDestination).AnyTimes()
	destination.EXPECT().Config().Return(connector.Config{}).AnyTimes()
	destination.EXPECT().Open(gomock.Any()).AnyTimes()
	destination.EXPECT().Teardown(gomock.Any()).AnyTimes()
	destination.EXPECT().Write(gomock.Any(), gomock.Any()).AnyTimes()
	destination.EXPECT().Ack(gomock.Any()).AnyTimes()
	destination.EXPECT().Errors().AnyTimes()
	return destination
}

func (s *serviceTestSetup) createPipeline(ctx context.Context, service *Service, status Status) (*Instance, connector.Source, connector.Destination, error) {
	plID := uuid.NewString()
	pl, err := service.Create(ctx, plID, Config{Name: fmt.Sprintf("%v pipeline %v", status, plID)})
	if err != nil {
		return nil, nil, nil, err
	}
	pl.Status = status

	// create mocked connectors
	ctrl := gomock.NewController(s.t)
	source := s.basicSourceMock(ctrl)
	destination := s.basicDestinationMock(ctrl)

	pl, err = service.AddConnector(ctx, pl, source.ID())
	if err != nil {
		return nil, nil, nil, err
	}

	pl, err = service.AddConnector(ctx, pl, destination.ID())
	if err != nil {
		return nil, nil, nil, err
	}

	return pl, source, destination, err
}

func TestService_Init_Simple(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	_, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)

	want := service.List(ctx)

	// create a new pipeline service and initialize it
	service = NewService(logger, db)
	err = service.Init(ctx, nil, nil)
	assert.Ok(t, err)

	got := service.List(ctx)
	assert.Equal(t, want, got)
	assert.Equal(t, len(got), 1)
}

func TestService_Init_Rerun(t *testing.T) {
	testCases := []struct {
		name     string
		status   Status
		expected Status
	}{
		{
			name:     "Running pipeline - running after restart",
			status:   StatusRunning,
			expected: StatusRunning,
		},
		{
			name:     "UserStopped pipeline - not running after restart",
			status:   StatusUserStopped,
			expected: StatusUserStopped,
		},
		{
			name:     "SystemStopped pipeline - running after restart",
			status:   StatusSystemStopped,
			expected: StatusRunning,
		},
		{
			name:     "StatusDegraded pipeline - not running after restart",
			status:   StatusDegraded,
			expected: StatusDegraded,
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			testServiceInit(t, tt.status, tt.expected)
		})
	}
}

func testServiceInit(t *testing.T, status Status, expected Status) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	setup := serviceTestSetup{t: t}
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	store := NewStore(db)

	service := NewService(logger, db)

	pl, source, destination, err := setup.createPipeline(ctx, service, status)
	assert.Ok(t, err)
	err = store.Set(ctx, pl.ID, pl)
	assert.Ok(t, err)

	// create a new pipeline service and initialize it
	service = NewService(logger, db)
	err = service.Init(
		ctx,
		testConnectorFetcher{
			source.ID():      source,
			destination.ID(): destination,
		},
		testProcessorFetcher{},
	)
	assert.Ok(t, err)

	// pipelines start asynchronously, give them some time to start
	time.Sleep(time.Millisecond * 100)

	got := service.List(ctx)
	assert.Equal(t, len(got), 1)

	for _, plGot := range got {
		if plGot.ID == pl.ID {
			// TODO remove this, only here to see why test fails on CI
			if expected != plGot.Status {
				t.Logf("status not same, error in pipeline: %s", plGot.Error)
			}
			assert.Equal(t, expected, plGot.Status)
		}
	}
}

func TestService_CreateSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	testCases := []struct {
		id     string
		name   string
		config Config
		want   *Instance
	}{{
		id:   uuid.NewString(),
		name: "full config",
		config: Config{
			Name:        "test-pipeline1",
			Description: "pipeline description",
		},
		want: &Instance{
			Config: Config{
				Name:        "test-pipeline1",
				Description: "pipeline description",
			},
			Status: StatusUserStopped,
		},
	}, {
		id:   uuid.NewString(),
		name: "empty description",
		config: Config{
			Name:        "test-pipeline2",
			Description: "",
		},
		want: &Instance{
			Config: Config{
				Name:        "test-pipeline2",
				Description: "",
			},
			Status: StatusUserStopped,
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(ctx, tt.id, tt.config)
			assert.Ok(t, err)

			tt.want.ID = got.ID
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestService_Create_PipelineNameExists(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	conf := Config{Name: "test-pipeline"}
	got, err := service.Create(ctx, uuid.NewString(), conf)
	assert.Ok(t, err)
	assert.NotNil(t, got)
	got, err = service.Create(ctx, uuid.NewString(), conf)
	assert.Nil(t, got)
	assert.Error(t, err)
}

func TestService_CreateEmptyName(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	got, err := service.Create(ctx, uuid.NewString(), Config{Name: ""})
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestService_GetSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	want, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)

	got, err := service.Get(ctx, want.ID)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestService_GetInstanceNotFound(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	// get pipeline instance that does not exist
	got, err := service.Get(ctx, uuid.NewString())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, ErrInstanceNotFound), "did not get expected error")
	assert.Nil(t, got)
}

func TestService_DeleteSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)

	err = service.Delete(ctx, instance)
	assert.Ok(t, err)

	got, err := service.Get(ctx, instance.ID)
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestService_List(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		instance, err := service.Create(ctx, uuid.NewString(), Config{Name: fmt.Sprintf("test-pipeline-%d", i)})
		assert.Ok(t, err)
		want[instance.ID] = instance
	}

	got := service.List(ctx)
	assert.Equal(t, want, got)
}

func TestService_UpdateSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)

	want := Config{
		Name:        "new-name",
		Description: "new description",
	}

	got, err := service.Update(ctx, instance, want)
	assert.Ok(t, err)
	assert.Equal(t, want, got.Config)
}

func TestService_Update_PipelineNameExists(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	_, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)
	instance2, err2 := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline2"})
	assert.Ok(t, err2)

	want := Config{
		Name:        "test-pipeline",
		Description: "new description",
	}

	got, err := service.Update(ctx, instance2, want)
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestService_UpdateInvalidConfig(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"})
	assert.Ok(t, err)

	config := Config{Name: ""} // empty name is not allowed

	got, err := service.Update(ctx, instance, config)
	assert.Error(t, err)
	assert.Nil(t, got)
}
