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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// fakeSchemaService records which service answered a call.
type fakeSchemaService struct {
	name string
}

func (f *fakeSchemaService) CreateSchema(_ context.Context, req pconnutils.CreateSchemaRequest) (pconnutils.CreateSchemaResponse, error) {
	resp := pconnutils.CreateSchemaResponse{}
	resp.Schema.Subject = f.name + ":" + req.Subject
	return resp, nil
}

func (f *fakeSchemaService) GetSchema(_ context.Context, req pconnutils.GetSchemaRequest) (pconnutils.GetSchemaResponse, error) {
	resp := pconnutils.GetSchemaResponse{}
	resp.Schema.Subject = f.name + ":" + req.Subject
	return resp, nil
}

func TestScopedSchemaService_RefusesUnscopedCalls(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := scopedSchemaService{}.GetSchema(ctx, pconnutils.GetSchemaRequest{Subject: "s", Version: 1})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)

	_, err = scopedSchemaService{}.CreateSchema(ctx, pconnutils.CreateSchemaRequest{Subject: "s"})
	ce, ok = conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)
}

func TestScopedSchemaService_ForwardsToContextService(t *testing.T) {
	is := is.New(t)
	a, b := &fakeSchemaService{name: "a"}, &fakeSchemaService{name: "b"}
	ctxA := contextWithSchemaService(context.Background(), a)
	ctxB := contextWithSchemaService(context.Background(), b)

	got, err := scopedSchemaService{}.GetSchema(ctxA, pconnutils.GetSchemaRequest{Subject: "s", Version: 1})
	is.NoErr(err)
	is.Equal(got.Schema.Subject, "a:s")

	created, err := scopedSchemaService{}.CreateSchema(ctxB, pconnutils.CreateSchemaRequest{Subject: "s"})
	is.NoErr(err)
	is.Equal(created.Schema.Subject, "b:s")

	// A nil service leaves the context unscoped.
	is.True(schemaServiceFromContext(contextWithSchemaService(context.Background(), nil)) == nil)
}

// TestDispenser_AdaptersCarrySchemaService: every call into a dispensed
// source or destination carries the dispenser's schema service.
func TestDispenser_AdaptersCarrySchemaService(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	svc := &fakeSchemaService{name: "rt"}

	mockSource := mock.NewSourcePlugin(ctrl)
	mockDestination := mock.NewDestinationPlugin(ctrl)
	d := NewDispenser(
		"mockPlugin",
		log.Nop(),
		func() pconnector.SpecifierPlugin { return nil },
		func() pconnector.SourcePlugin { return mockSource },
		func() pconnector.DestinationPlugin { return mockDestination },
	)
	d.schemaService = svc

	scoped := func(ctx context.Context) bool { return schemaServiceFromContext(ctx) == svc }

	mockSource.EXPECT().Open(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
			is.True(scoped(ctx))
			return pconnector.SourceOpenResponse{}, nil
		})
	mockDestination.EXPECT().Open(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
			is.True(scoped(ctx))
			return pconnector.DestinationOpenResponse{}, nil
		})

	src, err := d.DispenseSource()
	is.NoErr(err)
	_, err = src.Open(ctx, pconnector.SourceOpenRequest{})
	is.NoErr(err)

	dst, err := d.DispenseDestination()
	is.NoErr(err)
	_, err = dst.Open(ctx, pconnector.DestinationOpenRequest{})
	is.NoErr(err)
}

// TestNewDispenserFactory_NilServiceIsUnscoped guards against a typed nil
// pointer stored in the interface, which would panic on the first schema call
// instead of returning the coded error.
func TestNewDispenserFactory_NilServiceIsUnscoped(t *testing.T) {
	is := is.New(t)
	d := newDispenserFactory(DefaultBuiltinConnectors["github.com/conduitio/conduit-connector-log"], nil)("", pconnector.PluginConfig{}, log.Nop(), hostpolicy.Policy{})
	is.True(d.(*Dispenser).schemaService == nil)
}
