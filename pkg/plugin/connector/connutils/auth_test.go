// Copyright © 2024 Meroxa, Inc.
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

package connutils

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestTokenService_IsTokenValid(t *testing.T) {
	is := is.New(t)

	underTest := NewTokenService()

	token := underTest.GenerateNew("connector-id")

	err := underTest.IsTokenValid(token)
	is.NoErr(err)
}

func TestTokenService_Deregister(t *testing.T) {
	is := is.New(t)

	underTest := NewTokenService()

	token := underTest.GenerateNew("connector-id")
	underTest.Deregister(token)
	err := underTest.IsTokenValid(token)
	is.True(cerrors.Is(err, ErrInvalidToken))
}
