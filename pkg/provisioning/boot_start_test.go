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

package provisioning

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// bootStartingLifecycle is a LifecycleService that also implements
// bootStarter. Start is the embedded mock with no expectations, so any call
// to it fails the test.
type bootStartingLifecycle struct {
	*mock.LifecycleService
	bootStarted []string
}

func (l *bootStartingLifecycle) StartOnBoot(_ context.Context, id string) error {
	l.bootStarted = append(l.bootStarted, id)
	return nil
}

// TestService_Init_StartsThroughStartOnBoot: provisioning at startup starts a
// pipeline whose config says running through StartOnBoot when the lifecycle
// service has it (#2989), so a boot refusal is recorded as the pipeline's
// status. Calling Start instead fails on the mock's missing expectation.
func TestService_Init_StartsThroughStartOnBoot(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	lc := &bootStartingLifecycle{LifecycleService: mock.NewLifecycleService(ctrl)}
	s := &Service{logger: log.Nop(), lifecycleService: lc}

	is.NoErr(s.startOnBoot(context.Background(), "p1"))
	is.Equal(lc.bootStarted, []string{"p1"})
}

// TestService_Init_FallsBackToStart: a lifecycle service without StartOnBoot
// (pkg/lifecycle, the default engine) is started with Start.
func TestService_Init_FallsBackToStart(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	lc := mock.NewLifecycleService(ctrl)
	lc.EXPECT().Start(gomock.Any(), "p1").Return(nil)
	s := &Service{logger: log.Nop(), lifecycleService: lc}

	is.NoErr(s.startOnBoot(context.Background(), "p1"))
}
