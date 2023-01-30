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

package api

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/database/mock"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
	gh "google.golang.org/grpc/health/grpc_health_v1"
)

func TestHealthChecker_Check_OK(t *testing.T) {
	is := is.New(t)
	db := mock.NewDB(gomock.NewController(t))
	db.EXPECT().Ping(gomock.Any()).Return(nil)

	underTest := NewHealthChecker(db)
	resp, err := underTest.Check(context.Background(), &gh.HealthCheckRequest{})
	is.NoErr(err)
	is.Equal(gh.HealthCheckResponse_SERVING, resp.Status)
}

func TestHealthChecker_Check_UnknownService(t *testing.T) {
	is := is.New(t)
	db := mock.NewDB(gomock.NewController(t))

	underTest := NewHealthChecker(db)
	_, err := underTest.Check(context.Background(), &gh.HealthCheckRequest{Service: "foobar"})
	is.True(err != nil)
	is.Equal("rpc error: code = NotFound desc = service \"foobar\" not found", err.Error())
}
