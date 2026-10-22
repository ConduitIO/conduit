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

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

type recordingResolver struct {
	refuse error
	calls  []map[string]string
}

func (r *recordingResolver) ResolveSettings(_ string, settings map[string]string) (map[string]string, error) {
	r.calls = append(r.calls, settings)
	if r.refuse != nil {
		return nil, r.refuse
	}
	out := map[string]string{}
	for k, v := range settings {
		out[k] = "resolved:" + v
	}
	return out, nil
}

// Lifecycle events carry the settings the resolver produced, not the raw stored
// text, and a refusal fails the event before the plugin sees anything.
func TestSource_OnDelete_UsesResolver(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	rr := &recordingResolver{}
	src.Instance.settingsResolver = rr
	src.Instance.LastActiveConfig.Settings = map[string]string{"path": "/x"}

	var got map[string]string
	sourceMock.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
			got = req.Config
			return pconnector.SourceLifecycleOnDeletedResponse{}, nil
		})
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	is.NoErr(src.OnDelete(ctx))
	is.Equal(got, map[string]string{"path": "resolved:/x"})
}

func TestSource_OnDelete_ResolverRefusalStopsTheEvent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	refusal := cerrors.New("path not allowed")
	src.Instance.settingsResolver = &recordingResolver{refuse: refusal}
	src.Instance.LastActiveConfig.Settings = map[string]string{"path": "/etc/passwd"}

	// no LifecycleOnDeleted expectation: the plugin must not be called
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	err := src.OnDelete(ctx)
	is.True(cerrors.Is(err, refusal))
}

func TestDestination_OnDelete_ResolverRefusalStopsTheEvent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	dest, destMock := newTestDestination(ctx, t, gomock.NewController(t))
	refusal := cerrors.New("path not allowed")
	dest.Instance.settingsResolver = &recordingResolver{refuse: refusal}
	dest.Instance.LastActiveConfig.Settings = map[string]string{"path": "/etc/passwd"}

	destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)
	err := dest.OnDelete(ctx)
	is.True(cerrors.Is(err, refusal))
}
