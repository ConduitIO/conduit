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

package connector

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestSource_Read_SanitizesRecords is the regression test for #3107. Read used
// to sanitize a range-variable copy of each record, so a record whose
// connector left Metadata nil reached the pipeline without opencdc.readAt and
// conduit.source.connector.id. It also pins that nil Key/Before/After stay nil
// and that values the connector did set are not overwritten.
func TestSource_Read_SanitizesRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	is.NoErr(src.Open(ctx))

	in := []opencdc.Record{
		// Hand-built record: everything optional left nil.
		{Position: opencdc.Position("p1"), Operation: opencdc.OperationCreate},
		// Connector set readAt and its own connector ID, and an empty
		// (present) key: all three must survive untouched.
		{
			Position:  opencdc.Position("p2"),
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{},
			Metadata: opencdc.Metadata{
				opencdc.MetadataReadAt:                   "42",
				opencdc.MetadataConduitSourceConnectorID: "set-by-connector",
				"custom":                                 "kept",
			},
			Payload: opencdc.Change{After: opencdc.RawData("after")},
		},
		// Non-nil, empty metadata.
		{Position: opencdc.Position("p3"), Operation: opencdc.OperationDelete, Metadata: opencdc.Metadata{}},
	}
	// The in-memory stream is unbuffered: Send blocks until Read receives.
	sendErr := make(chan error, 1)
	go func() { sendErr <- stream.Server().Send(pconnector.SourceRunResponse{Records: in}) }()

	got, err := src.Read(ctx)
	is.NoErr(err)
	is.NoErr(<-sendErr)
	is.Equal(len(got), 3)

	for _, r := range []opencdc.Record{got[0], got[2]} {
		readAt, err := r.Metadata.GetReadAt()
		is.NoErr(err) // readAt must be set on records with no connector-provided value
		is.True(!readAt.IsZero())
		connID, err := r.Metadata.GetConduitSourceConnectorID()
		is.NoErr(err)
		is.Equal(connID, src.Instance.ID)
		is.True(r.Key == nil)            // nil key must stay nil, not become RawData{}
		is.True(r.Payload.Before == nil) // nil Before must stay nil
		is.True(r.Payload.After == nil)  // nil After must stay nil
	}

	is.Equal(got[1].Metadata[opencdc.MetadataReadAt], "42")
	is.Equal(got[1].Metadata[opencdc.MetadataConduitSourceConnectorID], "set-by-connector")
	is.Equal(got[1].Metadata["custom"], "kept")
	is.Equal(got[1].Key, opencdc.StructuredData{})
	is.True(got[1].Payload.Before == nil)
	is.Equal(got[1].Payload.After, opencdc.RawData("after"))
}
