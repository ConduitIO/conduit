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
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Codes for source batching configuration conditions that the connector SDK
// (v0.18.0 and later) reports. The SDK has no error-code mechanism: it logs
// these as WARNs today and, when a rule becomes a rejection, puts the code in
// the Configure error message. The engine registers the same reasons so agents
// and --json output can match the code instead of the text. SDKs that predate
// the codes never emit them, so the mapping below is a no-op for them.
//
// All three are InvalidArgument: they describe a combination of
// sdk.batch.size and sdk.batch.delay that the caller can change.
// The reasons are string literals, not the SDK's constants, because the
// llmsgen error-code scanner only reads literal Register arguments.
// TestSDKCodesMatchSDK keeps them equal to the SDK's.
var (
	// CodeSDKBatchUnboundedWait: sdk.batch.size above 0 with sdk.batch.delay 0
	// waits for a full batch with no deadline, so a sparse source can hold
	// records unsent and unacked indefinitely.
	CodeSDKBatchUnboundedWait = conduiterr.Register("sdk.batch.unbounded_wait", codes.InvalidArgument)
	// CodeSDKBatchSizeDefaulted: sdk.batch.delay was set while sdk.batch.size
	// is 0, so the SDK substituted a bounded batch size.
	CodeSDKBatchSizeDefaulted = conduiterr.Register("sdk.batch.size_defaulted", codes.InvalidArgument)
	// CodeSDKBatchDrainSemanticsPending: an explicit sdk.batch.size above 0 with
	// sdk.batch.delay 0 waits for a full batch today and will mean "return what
	// is already queued" in a later SDK release.
	CodeSDKBatchDrainSemanticsPending = conduiterr.Register("sdk.batch.drain_semantics_pending", codes.InvalidArgument)
)

// sdkCodeHints holds, per SDK code, the setting the caller changes and the fix.
// ConfigPath is relative to the connector's settings, because the plugin layer
// does not know where the connector sits in the pipeline.
var sdkCodeHints = map[string]struct {
	code       conduiterr.Code
	configPath string
	suggestion string
}{
	sdk.CodeBatchUnboundedWait: {
		code:       CodeSDKBatchUnboundedWait,
		configPath: "/settings/sdk.batch.delay",
		suggestion: "set sdk.batch.delay above 0 so a partial batch is sent, or set both sdk.batch.size and sdk.batch.delay to 0 to turn source batching off",
	},
	sdk.CodeBatchSizeDefaulted: {
		code:       CodeSDKBatchSizeDefaulted,
		configPath: "/settings/sdk.batch.size",
		suggestion: "set sdk.batch.size explicitly; with sdk.batch.size 0 the SDK substitutes a bounded size",
	},
	sdk.CodeBatchDrainSemanticsPending: {
		code:       CodeSDKBatchDrainSemanticsPending,
		configPath: "/settings/sdk.batch.delay",
		suggestion: "set sdk.batch.delay above 0 to keep full batches; sdk.batch.delay 0 will mean \"send what is already queued\" in a later release",
	},
}

// MapSDKCode classifies a connector Configure error. If the message carries a
// known SDK batching code as a delimited token (sdk.CodeFromMessage), it
// returns a *conduiterr.ConduitError with that code, the config path and a
// suggested fix, keeping the original message and wrapping err. Configure
// errors are wrapped by several layers before they reach the engine, so the
// code is found anywhere in the message, not only at the start.
//
// An error that already carries a ConduitError, an error without an SDK code,
// and nil are returned unchanged.
func MapSDKCode(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := conduiterr.Get(err); ok {
		return err // never shadow an inner code
	}
	reason, ok := sdk.CodeFromMessage(err.Error())
	if !ok {
		return err
	}
	hint, ok := sdkCodeHints[reason]
	if !ok {
		return err // a code this engine does not know yet
	}
	ce := conduiterr.WithCode(err, hint.code)
	ce.ConfigPath = hint.configPath
	ce.Suggestion = hint.suggestion
	return ce
}
