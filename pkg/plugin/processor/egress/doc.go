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

// Package egress implements the host side of the WASM host-mediated
// network-egress capability for standalone (WASM) processors, per
// docs/design-documents/20260726-wasm-host-egress-capability.md.
//
// This package IS the security boundary. Standalone processors are untrusted,
// possibly-third-party guest code with no socket API; egress means the host
// (which can reach cloud metadata, loopback, and the private VPC) makes an
// outbound request whose destination is influenced by that guest. That is the
// textbook Server-Side Request Forgery (SSRF) setup, so every request is
// enforced against a per-processor policy in two independent stages:
//
//   - Stage 1 (coarse, [Policy.MatchHostPort]): the request URL's scheme and
//     host:port must match an entry in the processor's resolved allowlist. This
//     is a fast reject and a usability aid — it is NOT the guarantee, because a
//     matched hostname can still resolve to a hostile address.
//
//   - Stage 2 (load-bearing, [Service.dialControl] via [Guard.Refuse]): a
//     net.Dialer.Control hook fires immediately before every connect(2), for
//     every candidate address the resolver returns, for http AND TLS dials
//     alike. It refuses the connection if the resolved IP is in a private,
//     loopback, link-local, reserved, or embedded-v4 (v4-mapped / NAT64 /
//     6to4 / Teredo) range, UNLESS the exact (IP, port) pair is an explicit
//     allowlist entry (the local-Ollama carve-out). Running per resolved IP at
//     dial time closes the DNS-rebinding / TOCTOU window a string allowlist
//     leaves open.
//
// Additional hardening the [Service] applies: the transport uses NO proxy
// (Transport.Proxy is pinned nil so HTTP(S)_PROXY / ALL_PROXY cannot bypass the
// dialer), redirects are not followed, Host / Authorization / Accept-Encoding
// are host-reserved (guest overrides rejected), the per-call timeout and the
// response-size cap (io.LimitReader on the decompressed stream) are
// host-enforced, and credentials are host-injected from a secret reference the
// guest names — the key never enters guest memory.
//
// Policy is bound per-processor: each processor instance gets its own Service
// with its own Policy captured in the dialer's Control closure. No egress
// policy is ever held on a shared registry field, so one pipeline's allowlist
// cannot leak to another (a tested isolation invariant).
package egress
