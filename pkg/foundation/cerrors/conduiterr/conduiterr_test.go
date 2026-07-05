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

package conduiterr_test

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func frameCount(err error) int {
	frames, _ := cerrors.GetStackTrace(err).([]cerrors.Frame)
	return len(frames)
}

// TestNew_CapturesStackTrace is the guard for the design's crux: a leaf
// ConduitError with no wrapped cause must still carry a stack trace. Without the
// cerrors.New in the constructor it would carry none (ConduitError is not an
// xerrors value), silently breaking observability.
func TestNew_CapturesStackTrace(t *testing.T) {
	is := is.New(t)
	err := conduiterr.New(conduiterr.CodeInvalidArgument, "id is required")
	is.True(frameCount(err) > 0)
	is.Equal(err.Error(), "id is required")
}

// TestBareLiteral_HasNoStackTrace documents why New/Wrap are mandatory: a raw
// struct literal carries no frame at all.
func TestBareLiteral_HasNoStackTrace(t *testing.T) {
	is := is.New(t)
	err := &conduiterr.ConduitError{Code: conduiterr.CodeInvalidArgument, Message: "x"}
	is.Equal(frameCount(err), 0)
}

func TestWrap_PreservesStackTraceFromCause(t *testing.T) {
	is := is.New(t)
	cause := cerrors.New("boom")
	err := conduiterr.Wrap(conduiterr.CodeInternal, "while doing x", cause)
	is.True(frameCount(err) > 0)
}

func TestWrap_GuaranteesFrameWithNilCause(t *testing.T) {
	is := is.New(t)
	err := conduiterr.Wrap(conduiterr.CodeInternal, "no cause", nil)
	is.True(frameCount(err) > 0)
}

// TestWrap_DoesNotShadowInnerCode enforces the no-nested-code invariant: wrapping
// an inner ConduitError with a more generic code must not hide the inner code, and
// unset structured fields are inherited from the inner error.
func TestWrap_DoesNotShadowInnerCode(t *testing.T) {
	is := is.New(t)
	inner := conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "no such plugin")
	inner.ConfigPath = "/connectors/0/plugin"

	outer := conduiterr.Wrap(conduiterr.CodeInternal, "provisioning failed", inner)

	is.Equal(outer.Code.Reason(), conduiterr.CodeConnectorPluginNotFound.Reason())
	is.Equal(outer.Code.GRPCCode(), conduiterr.CodeConnectorPluginNotFound.GRPCCode())
	is.Equal(outer.ConfigPath, "/connectors/0/plugin")
}

func TestGet_FindsConduitErrorThroughWrapping(t *testing.T) {
	is := is.New(t)
	base := conduiterr.New(conduiterr.CodeNotFound, "missing")
	wrapped := cerrors.Errorf("context: %w", base)

	ce, ok := conduiterr.Get(wrapped)
	is.True(ok)
	is.Equal(ce.Code.Reason(), conduiterr.CodeNotFound.Reason())
}

func TestGet_ReturnsFalseForPlainError(t *testing.T) {
	is := is.New(t)
	_, ok := conduiterr.Get(cerrors.New("plain"))
	is.True(!ok)
}

func TestRegistry_FoundationalCodesPresent(t *testing.T) {
	is := is.New(t)
	c, ok := conduiterr.LookupCode("connector.plugin_not_found")
	is.True(ok)
	is.Equal(c.Reason(), "connector.plugin_not_found")
	is.True(len(conduiterr.Codes()) >= 5)
}
