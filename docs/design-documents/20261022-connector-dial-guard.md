# Dial-time egress enforcement for built-in connectors

## Summary

`connectors.egress.allow` and the always-on refusal of cloud metadata addresses are decided today on
the text of a connector's settings when it is configured. A connector resolves the host name again
when it dials, so a name that passed the check can later resolve (DNS rebinding) to an internal or
metadata address and be connected to. The engine already owns the authoritative decision
(`connegress.Policy.DialContext`: resolve, vet every address, refuse what cannot be vetted, connect to
the vetted address). This design hands that function to connectors so that **every network connection
a built-in connector opens goes through it**.

The hand-off is a new package in `conduit-commons`, `dialer` (`WithDialContext`, `FromContext`), next to
`hostpolicy`. The engine puts the dial guard on the call context of the built-in connectors that connect
somewhere; the connectors (postgres, kafka, s3, mysql) install it on every client they build. No
`conduit-connector-sdk` change and no `conduit-connector-protocol` change.

Scope decision for v0.21, stated explicitly: **the dial guard binds built-in connectors only.** Standalone
connectors are separate processes; they receive no dialer, are not restricted by the allowlist, and the
engine logs that at startup when an allowlist is set. See _Standalone connectors_.

Risk tier: Tier 1 (security boundary on how connectors reach the network; it touches connection setup
on the data path but not record flow, acks, positions or serialization).

Roadmap: v0.21 connector egress controls. Decided 2026-10-22: configure-time checking alone is not
acceptable for v0.21.

## Context

- `pkg/foundation/connegress` implements the policy. `Check` is the authoritative, resolving decision
  taken at configure time; `Hint` is the name-only answer for validation requests;
  `Policy.DialContext` is the dial-time guard; adapters fit it to pgx, franz-go, an `http.Transport`
  and a MySQL driver.
- The engine already puts the policy on the call context of postgres, kafka and s3 (`WithPolicy`), but
  the connector releases never read a dialer, so the dial guard is not exercised in a running engine.
- Built-in connectors run in-process: `pkg/plugin/connector/builtin` wraps the compiled-in
  `sdk.Connector` and every call into it goes through `Registry.WithContextDecorator`. That is the same
  channel the file connector's opener, the schema service and `hostpolicy.Policy` already use.
- Standalone connectors run as child processes over gRPC. The only engine-to-plugin channels are the
  gRPC protocol and the plugin's environment variables (`client.WithEnvVar`). A Go `context.Context`
  cannot cross the process boundary.
- `hostpolicy` in `conduit-commons` is the precedent: a small, dependency-free package that carries an
  operator decision from the engine to built-in connectors through the context, with "absent means
  default" semantics for plugins and tests that run without an engine.

## Constraints

1. Every connection a connector opens must be covered: data, admin, metadata, replication/binlog,
   snapshot, credential fetches, reconnects and connections to brokers discovered later.
2. A pipeline config must not be able to set or clear the dialer (only code holding the context can).
3. Without a host-supplied dialer a connector must behave exactly as before (no new defaults, same
   timeouts and keep-alives). The seam may not change behavior for standalone use or older engines.
4. Releasable independently: `conduit-commons` first, then each connector, then the engine bump.
5. No speculative generality: one function type and two functions.
6. The public change must read as a neutral feature ("let the host supply a dialer").

## Decision

### The seam

```go
package dialer // github.com/conduitio/conduit-commons/dialer

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

func WithDialContext(ctx context.Context, dial DialContextFunc) context.Context
func FromContext(ctx context.Context) (DialContextFunc, bool)
```

`FromContext` returns `false` when the host supplied none; the connector then keeps its library
default. (A "default `net.Dialer`" return was rejected: the libraries' own defaults differ, pgx 30 s,
franz-go 10 s, the AWS SDK's transport keep-alive and Happy Eyeballs settings, and substituting a zero
`net.Dialer` would silently change behavior for everyone who runs without a host dialer.)

Connector contract (in the package doc): read the dialer in the call that builds the client, install it
on **every** client the connector creates, capture the function (a background reconnect has no
call context), and change nothing when absent.

### Engine side

`withEgressPolicy` (`pkg/conduit/connegress.go`) now also does
`dialer.WithDialContext(ctx, policy.DialContext)`. It is applied to postgres, kafka and s3, as the policy
already was. No new configuration.

`Policy.DialContext` applies to the policy as configured, including the no-allowlist case: metadata
addresses are always refused at the dial, and with an allowlist every resolved address must be allowed.

### Where the seam lives: `conduit-commons`, not `conduit-connector-sdk`

The SDK is what standalone connectors and the built-in ones link, but built-in connectors take their
operator decisions from `conduit-commons` (`hostpolicy`) and the SDK would add a second place to look.
The package has no dependency beyond the standard library, so the connectors take no new transitive
cost, and the SDK can re-export or populate it later (see _Standalone connectors_) without a seam
change. The SDK `Source`/`Destination` interfaces need no new method; that would be a protocol-adjacent
API change for something a context value carries.

### Standalone connectors

Two options were weighed: (a) pass the allowlist to the plugin through its environment and have the
plugin (SDK middleware) enforce it; (b) built-in only for v0.21.

(a) needs the policy engine (allowlist matching, metadata tables, NAT64/6to4/Teredo handling) to live in
a public shared module that every connector process links and trusts, and a new engine-to-plugin
contract (the env var format becomes public API). It also makes the SDK the enforcement point: a
standalone connector that does not honor the dialer, or an older SDK, is silently unrestricted, the
same cooperative model as built-ins but with a much larger population of third-party binaries.
That is a design of its own and does not fit before v0.21.

**Decision: (b).** For v0.21 the allowlist and dial guard bind built-in connectors only. This is not
silent: the engine logs a warning at startup when `connectors.egress.allow` is set, and
`docs/operations/connector-egress.md` states that standalone connectors (including the MySQL
connector, which is distributed standalone) are unrestricted. Refusing every standalone connector
whenever an allowlist is set was rejected for v0.21: it would break operators who run both, with no
supported way to proceed. The MySQL connector adopts the seam so it is covered the moment a host that
can supply the dialer (an embedding engine, a future env-driven SDK path, or MySQL becoming built-in)
runs it.

Follow-up (not in this change): the env-driven standalone path (a), tracked with #3043.

## Alternatives considered

1. **Wrap the connectors' network at the OS level (network namespace / seccomp / proxy).** Binds
   standalone connectors too, but needs platform-specific privileges, breaks `conduit` on macOS and
   Windows, cannot see the allowlist's host names, and Conduit is also run as a bare binary. Lost on
   portability and operational cost.
2. **A process-global dialer (`net.DefaultResolver`/`http.DefaultTransport` replacement, or a
   package-level variable in the connector).** No connector change needed for libraries that read global
   defaults, but pgx, franz-go and the AWS SDK build their own dialers, the engine hosts many connectors
   in one process, and a global cannot be per-connector or tested in isolation. Lost on coverage and
   isolation.
3. **A `Dialer`/`SetDialer` method on `sdk.Source`/`sdk.Destination`.** Explicit, but is a public SDK
   interface change that every connector and the protocol-adjacent middleware chain must carry, for one
   function. A context value is the existing engine-to-connector channel (`hostpolicy`, schema service,
   file opener). Lost on blast radius.
4. **Keep the setting check as the only enforcement and tell operators to list addresses.** Rejected by
   the 2026-10-22 decision.

## Failure modes

| Failure | Behavior |
| --- | --- |
| Name rebinds to a metadata address after configure | Refused at dial (`connector.egress_blocked`, no connection made). |
| Name allowlisted by CIDR rebinds outside the CIDR | Refused at dial. A name allowlisted _by name_ may resolve anywhere except metadata addresses: the operator trusted the name. |
| Name resolves to several addresses, one disallowed | The whole dial is refused (every address must be vetted). |
| Resolver down or name unresolvable at dial | Refused (cannot be vetted), the connector's normal reconnect/backoff applies. A DNS outage now also stops reconnects that previously used a cached or system-resolved address. |
| Connector release does not read the seam | Ignored; the setting check remains the only enforcement for that connector. Closed by the engine bumping to releases that honor it, and by a test (below) that fails if a built-in connector in `go.mod` does not. |
| Connector builds one client without the dialer (missed path) | That path is unguarded. Mitigated by per-connector tests that a refusing dialer stops _every_ connection type and by the engine e2e test per connector. |
| Dial to a host with several IPs, first unreachable | `Policy.DialContext` dials vetted addresses in order with the connector's per-attempt timeout; a dual-stack host with an unreachable first family connects slower than with Happy Eyeballs. Accepted; documented. |
| Proxy configured in the environment | The vetted address is dialed directly; HTTP proxies are not used by the guard's transport (same as processors). |
| Redirects, a Kafka cluster advertising other brokers | Every new connection still goes through the dial guard, so an advertised broker is vetted when dialed. Redirects across hosts are connector-level and unchanged. |
| Standalone connector | Not restricted. Logged at startup, documented. |

Data-path safety: the guard only decides whether a connection may be made. Refusal surfaces as a dial
error through the connector's existing error path (the pipeline errors and is restarted by the engine's
recovery), before any record is read or acked on that connection. Invariants 1 to 3 are unaffected: no
record is acknowledged or dropped by this change.

## Upgrade and rollback

- Additive everywhere. A connector with the seam run by an older engine, or standalone, sees no
  dialer and behaves as before. An engine that supplies the dialer run with connector releases that
  predate the seam behaves as before.
- Rollout order: `conduit-commons` minor release, then connector releases (postgres, kafka, s3, mysql),
  then the engine bumps them. Rolling back the engine bump restores the old behavior with no state
  or config migration; nothing serialized changes.
- v0.20.x: the line pins old connector releases (postgres v0.14.5, kafka v0.12.6, s3 v0.9.5) and
  `conduit-commons` v0.7.2. Covering it needs the seam backported to the commons v0.7 line and to each
  connector's v0.20-era line, plus new patch releases. This is a decision for the maintainer.

## Observability

- A refused dial is a coded `connector.egress_blocked` error naming the target and the config path of
  the setting when known, logged by the connector and visible in the pipeline error.
- Startup log (warn) when the allowlist is set: scope is built-in connectors only.
- Existing metric/alert guidance in `docs/operations/connector-egress.md` applies; the runbook entry
  is extended with the dial-time symptom (pipeline degraded with `connector.egress_blocked` on a
  previously working connector after a DNS change) and its remediation (list the new address or CIDR).

## Testing

- `conduit-commons`: unit tests and Examples for the seam.
- Each connector: a recording and a refusing dialer on the context for every connection type, including
  reconnects, with a listener that must see zero accepted connections on refusal; the no-dialer path
  is unchanged.
- Engine e2e (`pkg/conduit`, no network): the real postgres, kafka and s3 built-in connectors are
  configured through the registry with the engine's decorator and a fake resolver. After a successful
  configure the fake resolver rebinds the allowlisted name to a disallowed address; the connector's
  next dial is refused and the target listener sees no connection.
- The MySQL connector is not built in, so it has no engine e2e; its guard is covered in its own
  repository and through the policy's `MySQLDialContext` adapter test.
