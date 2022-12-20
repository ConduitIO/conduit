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

package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestSource_Ack_Deadlock(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	persister := NewPersister(
		logger,
		db,
		DefaultPersisterDelayThreshold,
		1,
	)

	ctrl := gomock.NewController(t)
	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test-plugin")).AnyTimes()

	connService := NewService(logger, db, persister)

	instance, err := connService.Create(
		ctx,
		"test-source",
		TypeSource,
		pluginDispenser,
		uuid.NewString(),
		Config{
			Name: "test-source",
			Settings: map[string]string{
				"recordCount":    "-1",
				"readTime":       "0ms",
				"format.options": "id:int",
				"format.type":    "raw",
			},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	sourceMock := mock.NewSourcePlugin(ctrl)
	sourceMock.EXPECT().Configure(gomock.Any(), instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().Start(gomock.Any(), nil).Return(nil)
	sourceMock.EXPECT().Ack(gomock.Any(), record.Position("test-pos")).Return(nil).Times(5)
	pluginDispenser.EXPECT().DispenseSource().Return(sourceMock, nil)

	conn, err := instance.Connector(ctx)
	is.NoErr(err)
	s, ok := conn.(*Source)
	is.True(ok)

	err = s.Open(ctx)
	is.NoErr(err)

	msgs := 5
	var wg sync.WaitGroup
	wg.Add(msgs)
	for i := 0; i < msgs; i++ {
		go func() {
			err := s.Ack(ctx, record.Position("test-pos"))
			wg.Done()
			is.NoErr(err)
		}()
	}

	if (*csync.WaitGroup)(&wg).WaitTimeout(ctx, 100*time.Millisecond) != nil {
		is.Fail() // timeout reached
	}
}
