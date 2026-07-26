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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
//     what the dialed server actually implements. If it doesn't, the dialed
//     server's gRPC layer rejects the hard-coded stub's service/method as
//     unknown (codes.Unimplemented) or the connection resolves to nothing
//     implementing it (codes.NotFound) - Validate classifies either as
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
// This cannot simply switch on the raw gRPC status code: conduit-connector-
// protocol's client stubs (pconnector/internal.UnwrapGRPCError, called from
// e.g. clientv1.SpecifierPluginClient.Specify) already strip the gRPC status
// out of the error before it ever reaches this package - verified directly
// against conduit-connector-protocol@v0.9.5's source, not assumed. A
// codes.Unimplemented status (exactly what a version mismatch produces:
// the hard-coded stub dialed a service the peer never registered)
// specifically becomes a plain error wrapping the sentinel
// pconnector.ErrUnimplemented ("%s: %w", st.Message(), ErrUnimplemented);
// every other status becomes a bare errors.New(st.Message()) with no status
// left to recover at all; and a failure with no status in the first place
// (a transport-level error - connection refused, DNS failure - before any
// status could be established) passes through UnwrapGRPCError completely
// unchanged. So: check the sentinel first (the one case conduit-connector-
// protocol preserves deliberately), and treat everything else - including
// any status that did survive, for robustness against a future protocol
// version that stops unwrapping - as unreachable, which is what it means in
// every case this package can actually observe today.
func classifySpecifyError(addr string, proto ProtocolVersion, err error) error {
	if cerrors.Is(err, pconnector.ErrUnimplemented) {
		return conduiterr.Wrap(
			conduiterr.CodeExternalConnectorVersionMismatch,
			fmt.Sprintf("external connector at %s does not implement pconnector protocol version %d", addr, proto),
			err,
		)
	}

	if st, ok := status.FromError(err); ok {
		switch st.Code() { //nolint:exhaustive // only classifying the codes this path can actually observe; everything else falls through to the default "unreachable" wrap below.
		case codes.Unimplemented, codes.NotFound:
			return conduiterr.Wrap(
				conduiterr.CodeExternalConnectorVersionMismatch,
				fmt.Sprintf("external connector at %s does not implement pconnector protocol version %d", addr, proto),
				err,
			)
		}
	}

	return conduiterr.Wrap(
		conduiterr.CodeExternalConnectorUnreachable,
		fmt.Sprintf("external connector at %s is unreachable", addr),
		err,
	)
}
