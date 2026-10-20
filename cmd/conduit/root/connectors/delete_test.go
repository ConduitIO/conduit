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

package connectors

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDeleteCommand_Args(t *testing.T) {
	is := is.New(t)
	c := DeleteCommand{}
	is.Equal(c.Args(nil).Error(), "requires a connector ID")
	is.Equal(c.Args([]string{"a", "b"}).Error(), "too many arguments")
	is.NoErr(c.Args([]string{"a"}))
	is.Equal(c.args.ConnectorID, "a")
}

// TestDeleteCommand_ForceSendsFlagAndRendersWarning: --force reaches the
// request, and the orphan warning is printed with its code (human) and
// carried as-is under --json.
func TestDeleteCommand_ForceSendsFlagAndRendersWarning(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	svc := mock.NewMockConnectorService(ctrl)
	resp := &apiv1.DeleteConnectorResponse{Warnings: []*apiv1.Warning{{
		Code:       "connector.orphaned_resources",
		Message:    `connector "c1" was deleted with force after its delete hook failed: could not drop slot "s1"`,
		Suggestion: "remove it by hand, then run 'conduit connectors delete c1 --acknowledge-orphan'",
	}}}
	svc.EXPECT().DeleteConnector(gomock.Any(), &apiv1.DeleteConnectorRequest{Id: "c1", Force: true}).Return(resp, nil)

	cmd := &DeleteCommand{args: DeleteArgs{ConnectorID: "c1"}, flags: DeleteFlags{Force: true}}
	got, err := cmd.ExecuteWithClientResult(context.Background(), &api.Client{ConnectorServiceClient: svc})
	is.NoErr(err)

	out := cmd.Render(got)
	is.True(strings.HasPrefix(out, "Connector c1 deleted.\n"))
	is.True(strings.Contains(out, "Warning [connector.orphaned_resources]"))
	is.True(strings.Contains(out, "--acknowledge-orphan"))

	// protojson varies its whitespace on purpose, so decode instead of
	// matching bytes.
	b, err := cecdysis.MarshalJSON(got)
	is.NoErr(err)
	var decoded struct {
		Warnings []struct {
			Code string `json:"code"`
		} `json:"warnings"`
	}
	is.NoErr(json.Unmarshal(b, &decoded))
	is.Equal(len(decoded.Warnings), 1)
	is.Equal(decoded.Warnings[0].Code, "connector.orphaned_resources")
}

func TestDeleteCommand_PlainDelete(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	svc := mock.NewMockConnectorService(ctrl)
	svc.EXPECT().DeleteConnector(gomock.Any(), &apiv1.DeleteConnectorRequest{Id: "c1"}).Return(&apiv1.DeleteConnectorResponse{}, nil)

	cmd := &DeleteCommand{args: DeleteArgs{ConnectorID: "c1"}}
	got, err := cmd.ExecuteWithClientResult(context.Background(), &api.Client{ConnectorServiceClient: svc})
	is.NoErr(err)
	is.Equal(cmd.Render(got), "Connector c1 deleted.\n")
}

// TestDeleteCommand_ForceAndAcknowledgeConflict: refused locally with a
// coded error, without calling the server.
func TestDeleteCommand_ForceAndAcknowledgeConflict(t *testing.T) {
	is := is.New(t)
	svc := mock.NewMockConnectorService(gomock.NewController(t)) // no expectations
	cmd := &DeleteCommand{args: DeleteArgs{ConnectorID: "c1"}, flags: DeleteFlags{Force: true, AcknowledgeOrphan: true}}

	_, err := cmd.ExecuteWithClientResult(context.Background(), &api.Client{ConnectorServiceClient: svc})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

func TestDeleteCommand_Acknowledge(t *testing.T) {
	is := is.New(t)
	svc := mock.NewMockConnectorService(gomock.NewController(t))
	svc.EXPECT().DeleteConnector(gomock.Any(), &apiv1.DeleteConnectorRequest{Id: "c1", AcknowledgeOrphan: true}).Return(&apiv1.DeleteConnectorResponse{}, nil)

	cmd := &DeleteCommand{args: DeleteArgs{ConnectorID: "c1"}, flags: DeleteFlags{AcknowledgeOrphan: true}}
	got, err := cmd.ExecuteWithClientResult(context.Background(), &api.Client{ConnectorServiceClient: svc})
	is.NoErr(err)
	is.Equal(cmd.Render(got), "Orphan entry for connector c1 acknowledged.\n")
}
