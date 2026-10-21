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

package connector_test

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// A refusal from the settings guard stops the validation request before any
// plugin is dispensed or configured.
func TestPluginService_Validate_SettingsGuardRefuses(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	refusal := cerrors.New("path refused")

	underTest := connector.NewPluginService(
		log.Nop(), mock.NewBuiltinReg(ctrl), mock.NewStandaloneReg(ctrl), mock.NewAuthManager(ctrl),
		connector.WithSettingsGuard(func(string, map[string]string) error { return refusal }),
	)

	// the mocks have no expectations: any call into a registry fails the test
	err := underTest.ValidateSourceConfig(context.Background(), "builtin:file", map[string]string{"path": "/etc/passwd"})
	is.True(cerrors.Is(err, refusal))
	err = underTest.ValidateDestinationConfig(context.Background(), "builtin:file", map[string]string{"path": "/etc/passwd"})
	is.True(cerrors.Is(err, refusal))
}
