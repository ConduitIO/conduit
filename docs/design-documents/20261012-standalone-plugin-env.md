# Restricting the environment of standalone connector plugins

## Summary

Standalone connector plugin processes receive the engine's full environment. This change restricts what
they receive: the engine stops inheriting the host environment into plugin processes and passes an
allowlist instead. The rollout is staged over three minors so existing deployments get a warning before
any behavior change:

- v0.21: default `inherit` (no behavior change), plus a startup WARN and a metric.
- v0.22: default flips to `allowlist`; `inherit` stays as an explicit escape hatch.
- v0.24: the `inherit` mode is removed.

Operators control it with `connectors.standalone.env-passthrough`. No `conduit-connector-protocol`
change and no SDK change.

## Context

Standalone connector plugins are separate OS processes, started by `go-plugin` through the protocol
client (`conduit-connector-protocol` v0.9.5, `pconnector/client/client.go`). The engine builds them in
`pkg/plugin/connector/standalone/registry.go` (`loadSpecifications` and the dispense path) with a handful
of `client.WithEnvVar` options (`CONDUIT_CONNECTOR_*`).

Facts, read at commit 81c6306 with `go-plugin` v1.8.0:

- The protocol client sets `cmd.Env = make([]string, 0)` under a comment saying the command gets a
  "clean env" (`client.go:44-45`).
- `go-plugin` then does `cmd.Env = append(cmd.Env, os.Environ()...)` unless `ClientConfig.SkipHostEnv` is
  set (`client.go:655-656`). Nothing in `pkg/` sets it, so the empty slice has no effect on what the
  plugin sees, and the comment does not describe the behavior.
- The same applies to the short-lived run that loads a plugin's specification when the plugin directory is
  scanned, which happens before any pipeline uses the plugin.
- Standalone (WASM) processors are separate: `pkg/plugin/processor/standalone/processor.go` passes only
  explicit `WithEnv` entries to wazero, so they are not affected and out of scope here.
- Built-in connectors run in-process and are out of scope.

So today a standalone plugin sees every variable the engine has: cloud credentials, API tokens, database
passwords, `CONDUIT_*` settings. That is wider than any plugin needs to do its job, and the engine's own
configuration surface (`CONDUIT_*`) is meant for the engine. `Option` in the protocol client is an exported
interface (`ApplyOption(*plugin.ClientConfig) error`), so the engine can set `SkipHostEnv` and add
variables with its own option, without touching the protocol module.

## Constraints

- Tier 1 (security boundary on the process boundary between the engine and third-party code).
- Must not change the connector protocol or require plugin rebuilds.
- Plugins that work today must keep working through at least two minors with a visible warning
  (announce, warn, remove; CLAUDE.md deprecation policy).
- Must work on Linux, macOS and Windows. Go's `os/exec` adds `SYSTEMROOT` on Windows when `Env` is
  non-nil and lacks it; we still list it explicitly because the allowlist must not rely on that.
- Must be explainable in one sentence to an operator: "plugins get a fixed list of variables, plus the
  ones you name".

## Decision

### Mechanism

An engine-side `client.Option` (in `pkg/plugin/connector/standalone`) that:

1. sets `ClientConfig.SkipHostEnv = true` when the mode is `allowlist`;
2. appends the allowlisted variables that are set in the engine's environment to `Cmd.Env`, in a stable
   order, before the `CONDUIT_CONNECTOR_*` options (so the engine's own entries are last and win on
   duplicates).

The same option is applied in both places a plugin process is started (specification load and dispense) so
discovery and runtime see the same environment.

### Allowlist

| Group | Variables |
| --- | --- |
| Process basics | `PATH`, `HOME`, `TMPDIR`, `TEMP`, `TMP`, `TZ` |
| Locale | `LANG`, `LC_*` |
| Network | `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and the lowercase forms |
| TLS roots | `SSL_CERT_FILE`, `SSL_CERT_DIR` |
| Windows | `SYSTEMROOT`, `windir` |
| Go runtime | `GODEBUG`, `GOMAXPROCS`, `GOMEMLIMIT` |
| Conduit | `CONDUIT_CONNECTOR_*` (set by the engine itself) |

Everything else, including `AWS_*`, `GOOGLE_*`, `AZURE_*`, `KUBERNETES_*`, `*_TOKEN`, `*_SECRET`,
`*_PASSWORD` and the engine's own `CONDUIT_*` settings, is not passed unless named by the operator.

### Config key

`connectors.standalone.env-passthrough` (flag `--connectors.standalone.env-passthrough`, env
`CONDUIT_CONNECTORS_STANDALONE_ENV_PASSTHROUGH`, YAML under `connectors.standalone`):

| Value | Meaning |
| --- | --- |
| `inherit` | Today's behavior: full host environment. Default in v0.21. |
| `allowlist` | `SkipHostEnv` plus the table above. Default from v0.22. |
| `allowlist:NAME1,NAME2,PREFIX_*` | The allowlist plus the named variables or prefixes, set engine-wide. |

Unknown values fail config validation with a stable error code, the config path and a suggestion listing
the valid values. `inherit` is accepted through v0.23 and rejected from v0.24. The setting is read at
startup; changing it needs a restart (same as `connectors.path`).

### Open question: per-connector opt-in (proposed, not decided)

An engine-wide `allowlist:NAMES` is coarse: one pipeline's need for `AWS_REGION` becomes every plugin's
environment. A finer option is a pipeline-connector setting, for example a reserved connector setting
`conduit.env-passthrough: "AWS_REGION,AWS_PROFILE"`, applying only to that connector's process. It would
live in pipeline config (a public contract), so it needs its own versioned schema change, and it lets a
pipeline author widen what a plugin receives, which weakens the operator's boundary unless it is capped by
the engine-wide setting. Proposal: ship only the engine-wide form in v0.22, and add the per-connector form
only if the first release shows real demand, with the operator setting acting as the upper bound.
DeVaris to decide whether to include it now.

## Alternatives considered

**A. Denylist of sensitive prefixes (`AWS_*`, `*_TOKEN`, ...).** Lost: a denylist fails open. Any secret
under a name nobody listed (`PGPASSWORD`, `MY_COMPANY_KEY`, a vendor token) is still inherited, and the list
needs permanent maintenance. The reverse failure (a missing allowlist entry) is loud and fixable; a missing
denylist entry is silent. We do use sensitive prefixes, but only to make the v0.21 warning useful, not as
the control.

**B. Per-connector env declaration in the connector specification.** The plugin declares which variables
it needs and the engine passes exactly those. Lost: the declaration comes from the party we are restricting,
so it cannot be the boundary, and it needs a protocol/spec change and a rebuild of every plugin. It is
still reasonable as an input to a future operator-visible review ("this plugin asks for X"), not as the
enforcement.

**C. Do nothing, document it.** Lost: the current "clean env" comment in the protocol client already
suggests the opposite of the behavior, so documentation alone leaves operators believing they have a
boundary that does not exist. Documentation is still part of the change, but it does not close the gap.

**D. Flip to the allowlist in v0.21 with no transition.** Lost: breaks working deployments without notice
(see failure modes). The staged rollout costs a release of continued exposure; we accept that and pair it
with the warning and docs so operators can opt in early.

**E. Sandboxing the plugin process (namespaces, seccomp, containers).** Not an alternative to env
restriction, a layer above it. Larger, platform-specific, and out of scope; this change is a prerequisite
for it being meaningful.

## Failure modes

Nothing here touches the record path: no acks, positions, ordering or serialized state change, so
invariants 1 to 7 are unaffected. The failures are start-up and configuration failures for plugins.

| Failure | Behavior | Mitigation |
| --- | --- | --- |
| Plugin relied on inherited cloud credentials (`AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`) | After the flip, plugin starts but auth fails at first connection; surfaces as the plugin's own error | v0.21 WARN names the prefixes in use; docs list `allowlist:NAMES`; preferred fix is putting credentials in connector config |
| Plugin needs proxy or custom CA settings | Proxy and `SSL_CERT_*` are in the allowlist; other settings (`CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`) are not | Name them via `allowlist:`; revisit the allowlist from first-release feedback |
| Plugin is a script or wrapper needing `PATH`, `HOME`, interpreter vars | `PATH`/`HOME` pass; interpreter-specific variables (`JAVA_HOME`, `PYTHONPATH`) do not | Named via `allowlist:`; documented |
| Windows | Missing `SYSTEMROOT`/`windir` breaks Winsock and many runtimes | Both allowlisted; add a Windows CI case for a plugin that opens a socket |
| Locale / timezone | Wrong collation or time rendering if `LANG`/`LC_*`/`TZ` dropped | All allowlisted |
| Variable set but empty | Passed through as empty, as today | n/a |
| Plugin env var collides with `CONDUIT_CONNECTOR_*` set by the engine | Engine entries are appended last and win | Test for duplicate keys |
| Specification load runs with a different env than dispense | Discovery succeeds, runtime fails or the reverse | One shared option applied at both call sites; test asserts both |
| Operator typos a name in `allowlist:` | Variable silently not passed | Startup log lists the resolved passthrough names; WARN for names not set in the engine's environment |
| Rollback after v0.22 | Operator sets `inherit` and restarts, no data migration | `inherit` available until v0.24; no persisted state involved |
| Older engine reads a config that has the new key | Unknown-key behavior depends on the old binary; unverified | Check before v0.21 release; document |

## Upgrade and rollback

- v0.21: config key added; default `inherit`; behavior identical to v0.20 except for log output and a
  metric. `allowlist` can be enabled by operators immediately.
- v0.22: default `allowlist`. Release notes lead with it, with the one-line fix for each failure above.
- v0.24: `inherit` removed; the value fails validation with an error pointing to `allowlist:NAMES`.
- Rollback at any point is a config change plus restart. No pipeline config, connector state or position
  format changes, so there is no migration and no upgrade/downgrade state test beyond starting an N-1
  config on N+1.

## Observability

- Startup WARN in `inherit` mode (once per engine start, not per plugin): states that standalone plugins
  receive the full engine environment, lists the count of variables passed and the names (never values) of
  those matching sensitive-looking prefixes (`AWS_`, `AZURE_`, `GOOGLE_`, `GCP_`, `KUBE`, `CONDUIT_`, and
  the suffixes `_TOKEN`, `_SECRET`, `_PASSWORD`, `_KEY`), and points at the config key. Names only, never
  values.
- Startup INFO in `allowlist` mode: the resolved list of operator-added names.
- Metric `conduit_standalone_plugin_env_vars_passed{mode}` (gauge, set at plugin start) so fleets can see
  which engines still inherit everything.
- Each start logs the number of variables passed to the plugin process at debug level.
- Runbook entry under `docs/operations/`: symptom "standalone plugin fails to authenticate or connect
  after upgrade", diagnosis (compare WARN list and plugin docs), remediation (`allowlist:NAMES` or move the
  setting into connector config).

## Interplay with ambient credentials (PR #3041)

The ambient-credentials design makes the built-in connectors' use of the host's cloud credentials an
explicit operator policy and states that standalone plugins fail closed: they get no policy value, so the
SDK treats ambient credentials as denied. That statement is only cooperative today, because a plugin that
inherits `AWS_*` can still read them directly. Once the allowlist lands (v0.22 default) the statement
becomes enforced for the environment path: `AWS_*` and equivalents are not in the plugin's environment
unless the operator names them with `allowlist:`. Naming them is then the explicit, auditable way to opt a
standalone plugin into host credentials, and it is consistent with the v0.22 standalone follow-up in that
design (engine-provided `CONDUIT_CONNECTOR_AMBIENT_*` variables, which fall under the allowed
`CONDUIT_CONNECTOR_*` prefix). Credentials delivered through a metadata endpoint or a mounted file are not
covered by an environment allowlist; that boundary stays with the ambient-credentials policy and any future
sandboxing.

## Testing plan

- Unit: allowlist resolution, `LC_*`/prefix matching, duplicate keys, mode parsing and error codes.
- Integration: a test plugin that dumps its environment; assert exact variable set in `inherit`,
  `allowlist`, and `allowlist:NAMES`, for both specification load and dispense.
- Windows case: plugin that opens a TCP connection with the allowlisted environment.
- Docs: `conduit.io` configuration reference, `llms.txt`, and the migration note, updated together.

## Consequences

- Plugin processes no longer see variables they were never meant to read once the default flips.
- Some existing plugin deployments need a one-line config change at the v0.22 upgrade.
- The misleading "clean env" comment in the protocol client becomes true for Conduit's use of it; the
  comment itself lives in another module and is left alone.

## Related

- `docs/design-documents/20261012-builtin-connector-ambient-credentials.md` (PR #3041)
- `go-plugin` v1.8.0 `ClientConfig.SkipHostEnv`
- Roadmap: v0.21 connector-certification
