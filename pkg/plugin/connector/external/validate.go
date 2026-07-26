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

package external

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Validate dials d's address (if not already connected) and performs a
// Specify round trip. This is what closes two gaps go-plugin's Reattach path
// leaves open, per Failure modes 1 and 4 of
// docs/design-documents/20260724-embed-slice2-external-connector.md:
//
//   - Reachability. go-plugin's Reattach never itself confirms the address
//     is live: grpc.Dial (called from go-plugin's dialGRPCConn) is
//     non-blocking by default, and plugin.Client.Start's Reattach branch
//     just records the address without dialing anything. The first real RPC
//     is what actually opens the TCP connection. Validate forces that to
//     happen at registration time, with an actionable error, instead of on
//     the first pipeline record.
//   - Version compatibility. go-plugin's Reattach never runs
//     checkProtoVersion, so nothing confirms the hard-coded
//     plugin.ClientConfig.Plugins stub set (see ProtocolVersion) matches
//     what the dialed server actually implements. If it doesn't, grpc-go
//     rejects the hard-coded stub's unknown service/method with
//     codes.Unimplemented - the only status an unknown gRPC service or
//     method actually produces (verified against grpc-go's source, not
//     assumed) - and Validate classifies that as
//     CodeExternalConnectorVersionMismatch rather than letting it surface as
//     a raw gRPC error on the first real record.
//
// Validate consumes the Dispenser: like standalone.Registry.loadSpecifications,
// it dispenses the specifier plugin and lets the terminal Specify call tear
// the connection down (specifierPluginDispenserSignaller in dispenser.go).
// A caller that needs the connection afterward (to actually dispense a
// source or destination) constructs a second, fresh Dispenser against the
// same address - see Dispenser's doc for why that mirrors the existing
// standalone contract rather than introducing a new one.
func (d *Dispenser) Validate(ctx context.Context) error {
	specifier, err := d.DispenseSpecifier()
	if err != nil {
		return conduiterr.Wrap(
			conduiterr.CodeExternalConnectorUnreachable,
			fmt.Sprintf("could not dispense specifier for external connector at %s", d.addr),
			err,
		)
	}

	_, err = specifier.Specify(ctx, pconnector.SpecifierSpecifyRequest{})
	if err != nil {
		return classifySpecifyError(d.addr.String(), d.proto, err)
	}

	return nil
}

// classifySpecifyError maps a failed Specify round trip to a stable,
// actionable ConduitError.
//
// This cannot switch on the raw gRPC status code at all: conduit-connector-
// protocol's client stubs (pconnector/internal.UnwrapGRPCError, called from
// e.g. clientv1.SpecifierPluginClient.Specify) already strip the gRPC status
// out of the error before it ever reaches this package - verified directly
// against conduit-connector-protocol@v0.9.5's source, not assumed. Exactly
// one status is special-cased there: codes.Unimplemented - the only status
// grpc-go actually returns for an unknown service or method (also verified
// against source, not assumed; there is no codes.NotFound case to worry
// about, unknown-service and unknown-method both come back as
// Unimplemented) - which is what dialing a hard-coded stub the peer never
// registered produces. UnwrapGRPCError turns that specific status into a
// plain error wrapping the sentinel pconnector.ErrUnimplemented ("%s: %w",
// st.Message(), ErrUnimplemented); every other status becomes a bare
// errors.New(st.Message()) with no status left to recover at all; and a
// failure with no status in the first place (a transport-level error -
// connection refused, DNS failure - before any status could be established)
// passes through UnwrapGRPCError completely unchanged. So the sentinel is
// not just the most convenient signal, it is the only one conduit-connector-
// protocol preserves - everything else this package can observe is
// classified as unreachable, which is what it means in every case that
// reaches here today.
func classifySpecifyError(addr string, proto ProtocolVersion, err error) error {
	if cerrors.Is(err, pconnector.ErrUnimplemented) {
		return conduiterr.Wrap(
			conduiterr.CodeExternalConnectorVersionMismatch,
			fmt.Sprintf("external connector at %s does not implement pconnector protocol version %d", addr, proto),
			err,
		)
	}

	return conduiterr.Wrap(
		conduiterr.CodeExternalConnectorUnreachable,
		fmt.Sprintf("external connector at %s is unreachable", addr),
		err,
	)
}
