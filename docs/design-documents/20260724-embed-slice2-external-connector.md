# Embed Slice 2 — external-connector failure-mode addendum

## Summary

This is a **gating addendum** to
[`docs/design-documents/20260724-embed-grpc-client-libraries.md`](20260724-embed-grpc-client-libraries.md)'s
Slice 2 ("External-connector engine feature → unlocks Case B") and its ADR,
[`docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md`](../architecture-decision-records/20260724-embed-bindings-via-grpc.md).
Slice 2 lets Conduit acquire a connector by **dialing a caller-provided address** instead of
spawning a binary — the mechanism `inline_source`/`inline_destination` (a host-language connector
running in-process inside the embedding application) is built on. That mechanism touches connector
acquisition, which sits directly upstream of the ack/position path (Invariants 1–3), so per
CLAUDE.md's Tier-1 bar this addendum's failure-mode analysis and reachability/security findings
must be resolved — explicitly, not assumed away — before Slice 2 code starts.

**Verdict on the parent doc's central claim, re-verified against the actual dependency source in
this repo's module cache (not the doc's own citations, independently re-read): the "external
connector = Conduit dials an address, no `conduit-connector-protocol` wire change" claim holds.**
See "The mechanism, verified against code" below for the exact evidence trail. This addendum also
surfaces two things the parent doc did **not** verify at the go-plugin source level, both of which
change what Slice 2 must build (not just document):

1. Go-plugin's default `Reattach` liveness/death detection (`cmdrunner.ReattachFunc`) is
   **local-PID-based** — it calls `os.FindProcess`/waits on a PID on the machine running the
   Conduit engine. This is meaningless for a genuinely remote host process (Mode 2 below) and
   Slice 2 cannot rely on it there; a custom `ReattachFunc`, or reliance on RPC-call failure alone,
   must be an explicit design choice, not a default nobody looked at.
2. The `Reattach` path performs **zero version-based plugin-set selection** — not merely a weaker
   check than the spawn path's, an _absent_ one. `checkProtoVersion` never runs on this path and
   `c.config.Plugins` is never derived from `VersionedPlugins`; Slice 2's dispenser must hard-code
   the stub set itself, unchecked by go-plugin against what the dialed server implements. Slice 2's
   "version mismatch" failure mode is not an "advisable to check" nicety — it is filling a gap
   go-plugin contributes nothing toward closing on this path.

Risk tier: **Tier 1-adjacent** (connector-acquisition path, protocol-adjacent). Design-ahead only:
no code ships from this doc. **DeVaris sign-off is required before Slice 2 implementation
starts** — this addendum gates that start, it does not authorize it.

## The mechanism, verified against code

### No `conduit-connector-protocol` wire change — stated definitively

Today, `pconnector/client.New(logger, path, opts...)`
(`conduit-connector-protocol@v0.9.5`, `pconnector/client/client.go:34-78`) builds:

```go
cmd := exec.CommandContext(context.Background(), path)
clientConfig := &plugin.ClientConfig{
    HandshakeConfig:  pconnector.HandshakeConfig,
    VersionedPlugins: map[int]plugin.PluginSet{ /* v1 -> clientv1 stubs, v2 -> clientv2 stubs */ },
    Cmd:              cmd,
    AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
}
return plugin.NewClient(clientConfig), nil
```

Conduit's own spawn-by-path dispenser (`pkg/plugin/connector/standalone/dispenser.go:39-62`,
`initClient`) calls exactly this `client.New`, and its registry
(`pkg/plugin/connector/standalone/registry.go:194-235`, `Registry.NewDispenser`) keys plugins by
filesystem `Path` (`registry.go:51-57`, `blueprint{FullName, Specification, Path}`).

Go-plugin (`github.com/hashicorp/go-plugin@v1.8.0`) has a second, first-class way to acquire the
same `plugin.Client`, re-read directly from the module cache for this addendum rather than taken
on the parent doc's line numbers:

- `ReattachConfig{Protocol, ProtocolVersion, Addr net.Addr, Pid int, ReattachFunc, Test}` —
  `client.go:299-315`.
- `Client.Start()` (`client.go:580`) dispatches on exactly one of `Cmd`, `Reattach`, or
  `RunnerFunc` being set (`client.go:596-615`: `"exactly one of Cmd, or Reattach, or RunnerFunc
  must be set"`); if `Reattach != nil`, `Start()` returns `c.reattach()` (`client.go:616`).
- `reattach()` (`client.go:972-1024`) sets `c.address = c.config.Reattach.Addr` and
  `c.protocol = c.config.Reattach.Protocol`, then returns `(c.address, nil)` — **the exact same
  `(net.Addr, error)` shape `Start()`'s spawn branch returns.** Everything downstream of `Start()` —
  gRPC dial, `VersionedPlugins` stub selection — is one shared code path regardless of which
  branch produced the address; `Reattach` only changes _how the address is obtained_, never what
  gRPC service definitions or `pconnector` wire messages get spoken once connected.
- Conduit's `Dispenser` interface (`pkg/plugin/connector/plugin.go:22-27`:
  `DispenseSpecifier/Source/Destination`) and the `pconnector.SourceRunStream`/
  `DestinationRunStream` types `Source`/`Destination` call `.Send`/`.Recv` on
  (`pkg/connector/source.go`, `pkg/connector/destination.go:193-230`) are unaware of how the
  underlying `plugin.Client` was constructed — they operate on the negotiated gRPC stream, not on
  `Cmd` vs. `Reattach`.

**Conclusion, stated as definitively as the evidence supports: a new dispenser variant — sibling
to `standalone.Dispenser`, building `plugin.ClientConfig{Reattach: &plugin.ReattachConfig{Addr:
...}}` instead of `{Cmd: cmd}` — requires zero changes to any `.proto` file, any
`pconnector.*Request`/`*Response` message, or any `clientv1`/`clientv2` stub. It is new **Go-side
client-construction code** in `pkg/plugin/connector/` (a new package, e.g.
`pkg/plugin/connector/external/`) plus a `config.Connector` field addition
(`pkg/provisioning/config/parser.go:40-47` today has `Connector{ID, Type, Plugin, Name, Settings,
Processors}` — no `Address` field yet; adding one is the only config-schema change Slice 2 needs).**

### Two go-plugin behaviors the parent doc did not verify, that change what Slice 2 must build

**(1) Default `Reattach` death-detection is local-machine-only.** When
`ReattachConfig.ReattachFunc` is nil, go-plugin defaults to
`cmdrunner.ReattachFunc(pid, addr)` (`client.go:975-978`,
`internal/cmdrunner/cmd_reattach.go:16-38`):

```go
func ReattachFunc(pid int, addr net.Addr) runner.ReattachFunc {
    return func() (runner.AttachedRunner, error) {
        p, err := os.FindProcess(pid)          // local OS PID lookup
        ...
        conn, err := net.Dial(addr.Network(), addr.String())  // confirms reachability, not liveness
        ...
        return &CmdAttachedRunner{pid: pid, process: p}, nil
    }
}
func (c *CmdAttachedRunner) Wait(_ context.Context) error { return pidWait(c.pid) }
```

`os.FindProcess`/`pidWait` operate on a PID **on the machine running the Conduit engine process**.
For Mode 1 (co-located host, below) this is correct and free. For Mode 2 combined with an inline
connector (a genuinely remote host process, potentially on a different machine or in a different
container/namespace), `Pid` is not a meaningful cross-machine identifier — reusing the default
`ReattachFunc` there would either misreport liveness (a coincidentally-matching local PID) or,
more likely, fail `os.FindProcess` for a PID that never existed on the engine's machine, treating
a perfectly healthy remote host connector as already-dead at dispense time. **Slice 2 must
therefore supply its own `ReattachFunc`** for any non-co-located registration (implementation
detail deferred to Slice 2's own design pass, but the two live options are: (a) a no-op/always-
alive `AttachedRunner` whose `Wait` blocks on nothing and relies entirely on RPC-call failure to
signal death — consistent with how Conduit detects a crashed _spawned_ plugin today, see Failure
mode 3 — or (b) an active health-check loop using go-plugin's built-in gRPC health client
(`GRPCClient.Ping()`, `grpc_client.go:127-130`, backed by `google.golang.org/grpc/health/
grpc_health_v1`, standard gRPC health-checking protocol — machine-agnostic by construction). Note
that Conduit does not call `Ping()`/`Exited()` anywhere in `pkg/plugin/` or `pkg/connector/` today
(grepped, no hits) — today's spawned-plugin crash detection is entirely "the next RPC call fails,"
not proactive. Slice 2 inherits that same passive posture for free if it picks option (a); option
(b) is new machinery and should not be built speculatively without a documented reason (CLAUDE.md's
no-speculative-generality rule) — see Failure mode 3 for the recommendation.

**(2) `Reattach` performs zero version-based plugin-set selection — the gap is "absent," not
merely "unverified."** On the spawn path, `Start()` parses the child's handshake line and calls
`checkProtoVersion(parts[1])` (`client.go:871`), which validates the **server's actual advertised
version** against `VersionedPlugins`' keys and — critically — the result _selects which
`PluginSet` the client actually dispenses symbols from_: `c.config.Plugins = pluginSet` and
`c.negotiatedVersion = version` (`client.go:879-880`).

On the `Reattach` path (`reattach()`, `client.go:972-1026`), `checkProtoVersion` is **never called
at all** — there is no handshake line to parse (no process was spawned to write one), and
`c.config.Plugins` is never touched by `reattach()`. The single `negotiatedVersion` assignment on
this path, `c.negotiatedVersion = c.config.Reattach.ProtocolVersion`, is gated by
`if c.config.Reattach.Test` (`client.go:1015-1016`) — a test-harness-only flag (its own doc
comment: "reattaching to ... a plugin in 'test mode'"). In the real, non-test `Reattach` path
Slice 2 would actually use, that branch is skipped entirely (`client.go:1020-1023`), so
**`negotiatedVersion` stays its zero value and `c.config.Plugins` is never derived from
`VersionedPlugins` at all.** The caller must supply `ClientConfig.Plugins` directly, hand-picked,
with zero help from go-plugin's version-negotiation machinery. **Conclusion: go-plugin does not
weakly trust a caller-supplied version on production `Reattach` — it performs no version-based
plugin-set selection whatsoever.** Whatever gRPC stub set Slice 2's dispenser hard-codes into
`ClientConfig.Plugins` is used unconditionally, with nothing in go-plugin checking it against what
the dialed server actually implements. This sharpens Failure mode 4 below from "a compatibility
nicety" to "a gap Slice 2 must close itself entirely" — go-plugin contributes nothing here to lean
on. The mitigation is unchanged and still sufficient: the external-connector registration path
must perform its own `Specify` round-trip immediately after dial and compare the returned
`Specification`'s protocol version against the `ClientConfig.Plugins` set the dispenser hard-coded,
failing registration on mismatch before the connector is wired into a running pipeline.

## `inline_source` / `inline_destination`

The host process (a Python client library driving `conduit.local()` or `conduit.connect(addr)`,
per the parent doc) runs the connector-authoring SDK's own gRPC server in-process — the identical
server shape `docs/design-documents/20260707-python-connector-sdk.md` already designs for
standalone connector plugins, just not spawned by Conduit and not writing a handshake line to a
pipe Conduit reads. The client library then registers that server's bound address with the (local
or remote) engine as an external connector. The host process is simultaneously: (a) the embedding
application, (b) the connector-server process, and (c) — for a source — the code producing
records via `SourceRunStream.Send`, or — for a destination — the code consuming records via
`DestinationRunStream.Recv` and returning acks. This is Case B from the parent doc; named
(non-inline) connectors (Case A) never instantiate this path and are entirely unaffected by
anything in this addendum.

## Reachability — the key failure surface

**Mode 1 — `conduit.local()`, subprocess co-located with the host, same machine.** The engine
subprocess and the host process share a loopback interface. Dialing `127.0.0.1:<port>` is
trivial, no firewall/NAT consideration applies, and the default `cmdrunner.ReattachFunc`'s
PID-based liveness check (see above) is valid, because the PID genuinely is local to the machine
running both processes. **This is the only mode Slice 2 should ship first-class, per the parent
doc's own framing: "`inline_*` connectors are a Mode-1 (local) feature first."**

**Mode 2 — `conduit.connect(addr)` to a remote/clustered engine.** The engine is a separately
deployed, independently managed service — potentially on a different host, in a different
container, behind a different network boundary (Kubernetes ingress/service mesh, a corporate
firewall, NAT). For an inline connector to work here, **the remote engine must be able to open an
outbound TCP connection back to the host process** — the reachability direction is inverted from
the usual "client dials server" shape most engineers expect (the _engine_ is the client of the
_host's_ connector server). Concretely, this fails or needs explicit operator work in:

- **NAT.** A host behind a home/office NAT with no port-forwarding is simply unreachable from a
  cloud-hosted engine — no amount of client-side configuration fixes this; the host needs a public
  or VPN-reachable address, or the direction must be inverted (host dials engine — not what Slice 2
  proposes; noted as a real alternative below).
- **Kubernetes.** The host process, if itself running in a pod, needs a stable Service (not a bare
  pod IP, which is ephemeral) and the engine's egress network policy must permit reaching that
  Service's namespace/port. If the host is _outside_ the cluster and the engine is _inside_, an
  Ingress only routes inbound HTTP(S) traffic by hostname/path — it is not designed for a
  raw-gRPC callback from inside the cluster to an arbitrary external address, so this shape likely
  needs a different ingress mechanism (a LoadBalancer Service on the host side, a tunnel) that
  Slice 2 must document as an explicit prerequisite, not assume works via "just open a port."
  Firewalls: any corporate/cloud-provider firewall between the two networks needs an explicit
  allow rule for the engine → host direction — the reverse of what's usually already open (host →
  engine, for the control-plane API itself).
- **Serverless / short-lived host processes.** A host running in a Lambda-like environment has no
  stable inbound address at all; `inline_*` structurally cannot work there regardless of firewall
  configuration.

**Per the parent doc, restated here as a hard gate, not a suggestion: Mode-2 support for
`inline_source`/`inline_destination` is explicitly NOT part of Slice 2's initial scope.** Slice 2
ships Mode 1 only. A Mode-2 story (if ever built) is a distinct follow-on design doc that must
independently solve the inverted-reachability problem — likely via a design that flips the dial
direction (host dials engine and _registers_ a stream, rather than engine dialing host), which is
a materially different mechanism from "engine dials a pre-known address" and would need its own
ADR, not a documentation footnote on this one. **Registration-time validation, not silent runtime
timeout, is mandatory for Mode 1 too:** the external-connector registration call must attempt to
open the TCP connection at `CreatePipeline`/registration time and fail fast with an actionable
`ConduitError` (stable code, e.g. `CodeExternalConnectorUnreachable`, the configured address, and a
suggestion) if it cannot — never defer discovery to the first record the pipeline tries to move.

## Host-connector lifecycle

- **Start ordering:** the host's in-process connector gRPC server must be listening and its
  address known **before** the client library issues the registration/`CreatePipeline` call that
  references it. The client library owns this ordering (start server → get bound port → build
  `PipelineConfig` with the address → deploy), not the engine — the engine has no way to wait for
  an address it doesn't have yet.
- **Who starts/stops it:** the embedding host process starts the in-process server (it's the
  host's own code/thread, per the Python connector SDK's server shape) and is responsible for
  stopping it — the engine never spawns or owns this process, unlike a standalone plugin binary it
  execs and can `Kill()` (`standalone/dispenser.go:76-90`, `teardown()`). This is the load-bearing
  asymmetry versus today's spawn model: Conduit's existing teardown path assumes it can always
  kill what it started; for an external connector it cannot, by construction.
- **Pipeline stop vs. host-server stop ordering:** the pipeline's own `StopPipeline`/graceful-
  shutdown path (`pkg/connector/source.go:204-245`'s `Teardown`, Invariant 7: flush any pending ack
  before tearing down the stream) must complete — i.e., the engine must have finished draining and
  acking in-flight records through the external connector — **before** the host process is allowed
  to stop its in-process server. If the host tears its server down first (e.g., the embedding
  application exits without calling `run.stop()`/waiting for it), the engine's Teardown call
  degrades to Failure mode 2/3 below (unreachable/dead mid-teardown) — the same bounded-wait,
  log-and-proceed fallback `Teardown`'s existing invariant-7 comment already documents for a
  stalled flush, not a new failure shape, but the client library's docs must state the ordering
  requirement plainly: **call `run.stop()` and wait for it before stopping the in-process
  connector server**, not the other way around.
- **Clean teardown, happy path:** engine's `StopPipeline` → Source/Destination `Teardown` (flush +
  final ack, existing invariant-7 path, transport-agnostic) → engine closes its gRPC connection to
  the external address → host library's `run.stop()` call returns → host application may now stop
  its in-process connector server. The registration itself (the address the engine holds) should be
  released/removed from the address-keyed registry at this point, not left dangling (see Failure
  mode 6).

## Failure modes (Tier-1-adjacent — the connector protocol carries acks/positions)

**1. Host endpoint unreachable at dial (registration time).** Covered above: must fail fast with
an actionable `ConduitError` at `CreatePipeline`/registration, never silently accepted and
discovered later. No data has moved yet, so no ack/position risk — purely a UX/fail-fast
requirement.

**2. Host endpoint drops mid-stream (network blip, not process death).** The gRPC stream errors on
`Send`/`Recv` (`pkg/connector/source.go`, `pkg/connector/destination.go:193-230` — the same call
sites that already handle a spawned plugin's stream breaking). This is **not new machinery**: it
is the existing "ordinary connector-plugin failure" path the parent doc correctly identifies —
pipeline transitions to `pipeline.StatusDegraded` (confirmed live: `pkg/lifecycle/service.go:893`,
`s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, ...)`, and equivalently
`pkg/lifecycle-poc/service.go:739`), with the error surfaced in `Pipeline.State.error`. Per
Invariant 3 (at-least-once floor), this must never silently drop a record — it degrades/halts,
consistent with today's behavior for any other connector-plugin fault. No new invariant-enforcement
code is needed here **provided** Slice 2 does not introduce a retry-and-swallow path that didn't
exist before; explicitly confirming that is part of Slice 2's own implementation-time review, not
assumed by this addendum.

**3. Host process dies mid-stream (position/ack safety — Invariants 1–3).** This is the failure
mode this addendum's code verification most changes the picture on. **Position/ack durability
itself is unaffected by Slice 2**: `Source.Ack` (`pkg/connector/source.go:379-410`) persists
`SourceState{Position: ...}` into Conduit's own Badger-backed store
(`pkg/connector/store.go`/`persister.go`) **before** the plugin-ack is sent back over the stream —
this logic lives entirely in the engine process and does not know or care whether the peer on the
other end of the stream is a spawned subprocess or a dialed external connector. A host process
dying mid-stream is therefore bounded by the exact same invariant machinery `tests/chaos`
(`045f283`) already SIGKILL-tests for a spawned plugin: the engine either already durably persisted
the position (safe to resume) or it didn't (the record is redelivered, per at-least-once). **What
changes is detection latency and mechanism**, per the go-plugin finding above: for Mode 1, the
default PID-based `Wait()` gives the engine a relatively fast, OS-level death signal (the local
process exited); if Slice 2 must supply a custom `ReattachFunc` for any non-default case (see
above), death may only be discovered via the next failed RPC call (Failure mode 2's path) — slower,
but not unsafe, since ack/position durability never depended on prompt death detection to begin
with (Invariant 1 already requires "ack only after destination confirms durable write," which by
construction tolerates an arbitrarily late destination-side failure). **Gate: Slice 2's design pass
must state explicitly which death-detection mechanism it uses (PID-`Wait`, a custom `ReattachFunc`,
or pure RPC-failure-detection) and confirm — with a chaos test extending the existing `tests/chaos`
suite (SIGKILL the _host_ process this time, not the engine or a spawned plugin) — that recovery
matches the already-proven spawned-plugin case before Slice 2 merges.** This addendum does not
authorize skipping that test.

**4. Version mismatch between host connector and engine.** Sharpened by the go-plugin finding
above: production `Reattach` performs **no version-based plugin-set selection at all** —
`checkProtoVersion` never runs on this path, and `c.config.Plugins` is never derived from
`VersionedPlugins` (`client.go:972-1026`, `1015-1016`; see "The mechanism" above). Slice 2's
dispenser must hard-code the `ClientConfig.Plugins` stub set itself, entirely unchecked by
go-plugin against what the dialed server actually implements. Slice 2 must close this gap
explicitly: immediately after a successful dial, perform a `Specify` round-trip
(`pconnector.SpecifierSpecifyRequest`, the same call `standalone.Registry.loadSpecifications`
already makes at `registry.go:181-191` for spawned plugins) and compare the returned
specification's supported protocol version against the `ClientConfig.Plugins` set the dispenser
hard-coded, failing registration with an actionable `ConduitError` on mismatch — before the
connector is wired into a running pipeline, not discovered as a decode error on the first real
record.

**5. Engine's `Stop` while the host connector is mid-write.** Two distinct cases:

- **Engine-initiated graceful stop (`StopPipeline`):** covered by the lifecycle ordering above —
  `Teardown`'s existing invariant-7 flush-and-wait (`pkg/connector/source.go:204-245`) applies
  unchanged; it is transport-agnostic (it operates on the persister and the stream, neither of
  which know about `Cmd` vs. `Reattach`). The bounded-wait/log-and-proceed fallback already
  documented there for a stalled flush applies identically to a slow-but-alive external connector.
- **Host-initiated disconnect while the engine is mid-write (a destination `Write` in flight,
  `pkg/connector/destination.go:193-209`, `d.stream.Send`) or mid-read (a source blocked in
  `Recv`):** an in-flight `Send`/`Recv` returns an error, which is Failure mode 2/3's path — the
  record being written is not acked by the destination (correctly — it wasn't durably written) and
  the pipeline degrades; the record is redelivered on retry/resume per at-least-once. No silent
  loss, provided (per Failure mode 2) Slice 2 doesn't introduce a swallow-and-continue path.

**6. Double-dial / stale address.** An address-keyed registry entry (per the parent doc's "registry
entry keyed by address instead of path") raises a question standalone's path-keyed registry never
faced: **addresses are far more likely to be reused than filesystem paths** (ephemeral ports,
container restarts reassigning the same host:port, a second pipeline registering the same host
process's address for a different logical connector). Slice 2 must define: (a) whether the same
address can be registered by more than one pipeline concurrently (if the host process multiplexes
several logical sources/destinations over one server, this may be legitimate — needs an explicit
per-connector identifier beyond the bare address, not just `host:port`); (b) what happens if a new
registration reuses an address a _previous, since-torn-down_ pipeline held — the registry must not
silently dispense a stale, already-dead connection, meaning the address-keyed registry entry needs
its own liveness check (or at minimum a fresh dial) at _dispense_ time, not just at initial
registration time, unlike the path-keyed registry which re-execs a fresh process on every dispense
(`standalone/dispenser.go`'s `dispense()`/`teardown()` cycle already handles this for the spawn
case by construction — a `Reattach`-based dispenser reusing a stale cached `plugin.Client` would
not).

## Security — dialing an arbitrary address

Slice 2 introduces the engine dialing a caller-supplied `host:port` and immediately speaking the
full `pconnector` protocol to whatever answers — a materially different trust posture from
spawning a binary the engine's own operator placed in a plugin directory
(`standalone.Registry`'s `pluginDir`, `registry.go:37-90`) or from a signed registry entry
(per the registry-build work referenced in memory). Concrete requirements, none currently designed:

- **Who may configure an `address:` connector.** This is pipeline-config territory
  (`config.Connector`), so anyone with permission to create/update a pipeline can point the engine
  at an arbitrary address. If Conduit ever ships multi-tenant pipeline authoring (fleet console,
  API tokens scoped to a subset of users), an external-connector address is an SSRF-shaped vector —
  the engine becomes a confused deputy dialing an internal address on behalf of whoever authored
  the pipeline config. **This must be scoped in the same permission model as `CreatePipeline`
  itself** (no separate, weaker gate), and — if Conduit's deployment model ever includes untrusted
  pipeline authors — needs an explicit allow/deny-list or network-policy story before Mode 2 (or
  even Mode 1 in a shared/multi-tenant `conduit.local()`) ships. Deferred to that point, not solved
  here, but the risk must be named now.
- **Authenticating the far end.** Nothing in the mechanism as designed verifies that whatever
  answers at the dialed address is actually the host's intended connector server and not something
  else bound to that port (accidentally or maliciously) between registration and dial. At minimum,
  the `Specify` round-trip added for Failure mode 4 gives a weak authenticity signal (it must
  return a well-formed `Specification`); a stronger story (mTLS between engine and external
  connector, a shared registration token analogous to `pconnutils.EnvConduitConnectorToken` already
  used for spawned plugins per `registry.go:174/230`) is an open question for Slice 2's own design
  pass, not resolved by this addendum. **This requirement is stronger than it first looks, because
  one of go-plugin's own integrity mechanisms is structurally unavailable here:**
  `Client.Init`/`Start()` hard-fails if both `SecureConfig` and `Reattach` are set —
  `if c.config.SecureConfig != nil && c.config.Reattach != nil { return nil, ErrSecureConfigAndReattach }`
  (`client.go:606-607`). `SecureConfig` is go-plugin's checksum-based binary-integrity check (verify
  the executable's hash before running it) — the spawn path's baseline defense against "someone
  swapped the plugin binary." **That defense does not merely need reinforcing for external
  connectors; it is unavailable to them by construction.** There is no binary to checksum (nothing
  is executed — a pre-running server is dialed), so authenticating the far end cannot fall back on
  "verify the artifact" the way a spawned plugin can; it must be an on-the-wire property (mTLS,
  token) from the start, not a defense-in-depth addition to a checksum baseline that doesn't exist
  here.
- **Encrypting the channel.** Corrected from an earlier "unaddressed" framing: `TLSConfig` is
  **available today, not unknown, for `Reattach`.** `newGRPCClient` — the single call site that
  actually dials the gRPC connection, shared by both the spawn and `Reattach` paths
  (`client.go:463`) — calls `dialGRPCConn(c.config.TLSConfig, c.dialer, ...)`
  (`grpc_client.go:59`), downstream of `Start()` for either path. By this addendum's own
  "everything downstream of `Start()` is shared" logic (see "The mechanism" above), `TLSConfig`
  therefore already applies transparently to `Reattach` connections exactly as it does to spawned
  ones — no go-plugin code change needed to encrypt the channel. **What remains open is not
  whether TLS works, but how certs get provisioned and trusted**: a shared CA between the host
  library and the engine, cert distribution at `conduit.local()`/`conduit.connect()` setup time,
  and rotation — a real design gap, but a provisioning problem, not a mechanism gap. This is an
  open question for Slice 2's own design pass, not resolved by this addendum, and matters more as
  Mode 2 is considered (loopback-only Mode 1 makes it lower-stakes, though not zero-stakes on a
  shared multi-tenant host).

## Upgrade / rollback

No serialized/persisted format changes: `config.Connector`'s new `Address` field is an addition to
an existing struct governed by `parser.go`'s own announce → warn → remove policy
(`parser.go:22-29`), not a new wire encoding. Positions/state persisted for an external connector
use the identical `SourceState`/Badger-backed store as any other connector
(`pkg/connector/source.go:227`, `store.go`) — an engine upgrade that changes nothing about
position serialization is unaffected by whether a given connector was spawned or dialed. The one
new compatibility surface is the `Specify`-round-trip version check added in Failure mode 4: it
must itself follow the same "fail loud, not silently degrade" discipline the rest of this doc
requires, and any change to what `Specify` returns is subject to the same protocol-versioning
discipline `conduit-connector-protocol` already has (CLAUDE.md: "never change
`conduit-connector-protocol` without an explicit versioning discussion" — Slice 2 adds no such
change, but a future revision to the version-check logic itself would need one).

## Observability

- `run.status()`/`GetPipeline` already surfaces `Pipeline.State.{status, error}` — an external
  connector reaching `StatusDegraded` is visible through the exact same call as any other connector
  failure; no new RPC needed for the happy/sad-path status story.
- The registration-time reachability check (Failure mode 1) and the `Specify`-round-trip version
  check (Failure mode 4) must both produce structured `ConduitError`s with stable codes so the
  failure is actionable via CLI/API/MCP — consistent with CLAUDE.md's "errors are API" convention —
  not a bare Go error string.
- A new metric/log line distinguishing "external connector unreachable at registration" from
  "external connector disconnected mid-run" from "spawned-plugin process crashed" would materially
  help operators triage — today's plugin-failure telemetry does not need to distinguish transport,
  but Slice 2 changes that; scoping this metric is part of Slice 2's own design pass.

## Gate: what must be true before Slice 2 code starts

1. Slice 2's own design doc names its death-detection mechanism (Failure mode 3) and ships a chaos
   test (SIGKILL the external host process) proving recovery matches the spawned-plugin case.
2. The `Specify`-round-trip version check (Failure mode 4) is designed, not deferred.
3. Registration-time reachability validation (fail-fast, actionable error) is designed, not
   deferred to a runtime timeout.
4. Mode 2 (`inline_*` against a remote engine) is explicitly out of scope for Slice 2's first cut —
   documented as such in the client-library docs before first release, not silently unsupported.
5. The security posture (who may register an `address:` connector, under what permission model) is
   at least named as an open risk in Slice 2's own design doc, even if not fully solved before a
   single-tenant `conduit.local()` ships.
6. Double-dial/stale-address handling (Failure mode 6) has an explicit answer, not an implicit
   "the registry probably handles it" assumption.

## Open questions for DeVaris

1. **Death-detection mechanism for Mode 1's `Reattach`.** Custom `ReattachFunc` relying purely on
   RPC-failure detection (matches today's spawned-plugin posture, zero new machinery), or invest in
   active `Ping()`-based health-checking (new machinery, faster detection, no precedent elsewhere
   in the codebase)? This addendum recommends the former on no-speculative-generality grounds, but
   it's a real trade-off (detection latency vs. new code) worth an explicit call.
2. **Is Mode 2 for `inline_*` a real near-term ask, or should Slice 2's docs foreclose it more
   forcefully** (e.g., a `local()`-only guard in the client library that rejects `inline_*` against
   `connect()`) rather than leaving it as "not yet solved but maybe someday"? A hard guard is
   cheaper to build than the honest-but-open framing this addendum currently uses.
3. **Multi-tenant / SSRF exposure timeline.** Does anything on the roadmap (fleet console API
   tokens, hosted Conduit) make the "arbitrary address dial" security gap in this addendum urgent
   before Slice 2 ships, or is single-operator `conduit.local()` the only real near-term deployment
   shape, making this a documented-but-deferred risk for now?

## Related

- `docs/design-documents/20260724-embed-grpc-client-libraries.md` — the design doc this addendum
  gates Slice 2 of; see its "External-connector feature (Slice 2)" and "Deployment modes" sections,
  which this addendum expands with independently re-verified go-plugin behavior and a fuller
  failure-mode enumeration.
- `docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md` — the ADR authorizing
  the gRPC-client-library direction this addendum's Slice 2 sits under.
- `docs/design-documents/20260707-python-connector-sdk.md` — the connector-authoring SDK server
  shape `inline_source`/`inline_destination` reuses in-process.
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` — the invariant-7 graceful-
  shutdown flush-and-wait logic this addendum's lifecycle-ordering section builds on unchanged.
- `pkg/plugin/connector/standalone/dispenser.go`, `registry.go` — the spawn-by-path dispenser
  Slice 2 adds a dial-by-address sibling to; this addendum cites their exact teardown/liveness
  behavior as the baseline Slice 2 must match or knowingly diverge from.
- `pkg/plugin/connector/plugin.go:22-27` — the transport-agnostic `Dispenser`/`SourcePlugin`/
  `DestinationPlugin` interfaces both the spawn and (future) address-based dispenser implement.
- `pkg/connector/source.go:204-245, 379-410`, `pkg/connector/destination.go:193-230` — the
  ack/position/teardown invariant-enforcement sites this addendum confirms are transport-agnostic.
- `pkg/provisioning/config/parser.go:40-47` — the `config.Connector` struct Slice 2 adds an
  `Address` field to.
- `pkg/lifecycle/service.go:893`, `pkg/lifecycle-poc/service.go:739`, `pkg/pipeline/instance.go:28`
  — the existing `StatusDegraded` path this addendum confirms an external-connector failure reuses
  unchanged.
- `github.com/hashicorp/go-plugin@v1.8.0`: `client.go:299-315` (`ReattachConfig`), `client.go:580-
  616` (`Start()` dispatch), `client.go:606-607` (`ErrSecureConfigAndReattach` — `SecureConfig` and
  `Reattach` are mutually exclusive), `client.go:972-1026` (`reattach()`), `client.go:871, 879-880`
  (spawn-path `checkProtoVersion` + `Plugins`/`negotiatedVersion` assignment), `client.go:1015-1016`
  (reattach-path `negotiatedVersion` assignment gated by `Reattach.Test`, a test-only flag — never
  runs in production, and `checkProtoVersion`/`c.config.Plugins` derivation never run on this path
  at all), `client.go:463` and `grpc_client.go:59` (`newGRPCClient`/`dialGRPCConn(c.config.TLSConfig,
  ...)` — the single shared dial call site downstream of `Start()` for both `Cmd` and `Reattach`,
  confirming `TLSConfig` already applies transparently to `Reattach`),
  `internal/cmdrunner/cmd_reattach.go:16-38` (default `ReattachFunc`, local-PID-based),
  `grpc_client.go:127-130` (`Ping()` via `grpc_health_v1`, unused elsewhere in this codebase today)
  — all read directly from the module cache for this addendum, not taken from the parent doc's
  citations.
- `github.com/conduitio/conduit-connector-protocol@v0.9.5`: `pconnector/client/client.go:34-78`
  (`ClientConfig` construction, identical `VersionedPlugins` regardless of `Cmd`/`Reattach`).
