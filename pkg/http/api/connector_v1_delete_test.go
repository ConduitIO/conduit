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

package api

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apimock "github.com/conduitio/conduit/pkg/http/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// TestConnectorAPIv1_DeleteConnector_HookFailure: the coded error survives
// the gRPC boundary with its code and suggestion (#3058).
func TestConnectorAPIv1_DeleteConnector_HookFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	csMock := apimock.NewConnectorOrchestrator(gomock.NewController(t))
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()

	hookErr := conduiterr.New(connector.CodeConnectorDeleteHookFailed, "could not delete connector: its delete hook failed")
	hookErr.Suggestion = "delete with --force"
	csMock.EXPECT().Delete(ctx, id).Return(hookErr)

	_, err := api.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{Id: id})
	st, ok := grpcstatus.FromError(err)
	is.True(ok)
	is.Equal(st.Code(), codes.FailedPrecondition)
	ce := conduiterr.FromStatus(st)
	is.Equal(ce.Code, connector.CodeConnectorDeleteHookFailed)
	is.Equal(ce.Suggestion, "delete with --force")
}

// TestConnectorAPIv1_DeleteConnector_ForceWarns: a forced delete whose hook
// failed returns the connector.orphaned_resources warning.
func TestConnectorAPIv1_DeleteConnector_ForceWarns(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	csMock := apimock.NewConnectorOrchestrator(gomock.NewController(t))
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()

	csMock.EXPECT().ForceDelete(ctx, id).Return(&connector.Orphan{
		Version: connector.OrphanVersion, ConnectorID: id, Type: "source", Plugin: "builtin:postgres",
		PipelineID: "p1", HookError: `could not drop slot "conduit_orders"`,
	}, nil)

	got, err := api.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{Id: id, Force: true})
	is.NoErr(err)
	is.Equal(len(got.Warnings), 1)
	is.Equal(got.Warnings[0].Code, "connector.orphaned_resources")
	is.True(strings.Contains(got.Warnings[0].Message, `"conduit_orders"`))
	is.True(strings.Contains(got.Warnings[0].Suggestion, "--acknowledge-orphan"))
}

// TestConnectorAPIv1_DeleteConnector_ForceHookOK: force with a working hook
// is an ordinary delete, no warning.
func TestConnectorAPIv1_DeleteConnector_ForceHookOK(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	csMock := apimock.NewConnectorOrchestrator(gomock.NewController(t))
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()

	csMock.EXPECT().ForceDelete(ctx, id).Return(nil, nil)

	got, err := api.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{Id: id, Force: true})
	is.NoErr(err)
	is.Equal(len(got.Warnings), 0)
}

func TestConnectorAPIv1_DeleteConnector_AcknowledgeOrphan(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	csMock := apimock.NewConnectorOrchestrator(gomock.NewController(t))
	api := NewConnectorAPIv1(csMock, nil)
	id := uuid.NewString()

	csMock.EXPECT().AcknowledgeOrphan(ctx, id).Return(nil)
	got, err := api.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{Id: id, AcknowledgeOrphan: true})
	is.NoErr(err)
	is.Equal(len(got.Warnings), 0)

	// Both flags at once is a bad request; nothing is called.
	_, err = api.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{Id: id, AcknowledgeOrphan: true, Force: true})
	st, ok := grpcstatus.FromError(err)
	is.True(ok)
	is.Equal(st.Code(), codes.InvalidArgument)
}
