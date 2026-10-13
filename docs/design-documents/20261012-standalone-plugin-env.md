# Restricting the environment of standalone connector plugins

## Summary

Least privilege: a standalone connector plugin should receive the environment it needs to run, and
nothing more. This change has the engine pass a fixed allowlist of variables (plus any the operator
names) to plugin processes instead of the engine's whole environment. It reduces what a plugin receives
by default. It is not a sandbox; a same-user plugin can reach other sources of the same data.

Operators control it with `connectors.standalone.env-passthrough`. The same filtered environment applies
to every place the engine starts a plugin process (see _Mechanism_). No `conduit-connector-protocol`
change and no SDK change. `allowlist` is the default from v0.21; see _Rollout_.

## Context

Standalone connector plugins are separate OS processes started by `go-plugin` through the protocol client
(`conduit-connector-protocol`). The engine passes a handful of `CONDUIT_CONNECTOR_*` variables to them
through `client.WithEnvVar` options. Operators have no control over the rest of a plugin's environment,
and a plugin needs only a small part of it. The engine's own configuration surface (`CONDUIT_*`) is meant
for the engine, not for plugins.

`Option` in the protocol client is an exported interface, so the engine can restrict the environment and
add variables with its own option, without touching the protocol module.

Out of scope: standalone (WASM) processors, which already receive only explicit entries, and built-in
connectors, which run in-process. Built-in connector credential policy is covered separately (PR #3041),
which does not apply to standalone plugins.

## Constraints

- Tier 1 (the process edge between the engine and third-party code). Hardening, not a security boundary.
- Must not change the connector protocol or require plugin rebuilds.
- Deprecation policy (announce, warn, remove over at least two minors) applies to any removed
  `inherit` mode. `inherit` is not removed; the default flip is covered by _Rollout_.
- Must work on Linux, macOS and Windows. Go's `os/exec` adds `SYSTEMROOT` on Windows when `Env` is
  non-nil and lacks it; we still list it explicitly because the allowlist must not rely on that.
  Variable names are case-insensitive on Windows, so matching there is case-insensitive.
- Must be explainable in one sentence to an operator: "plugins get a fixed list of variables, plus the
  ones you name".

## Decision

### Mechanism

An engine-side `client.Option` (in `pkg/plugin/connector/standalone`) that:

1. sets `ClientConfig.SkipHostEnv = true` when the mode is `allowlist`;
2. appends the allowlisted variables that are set in the engine's environment to `Cmd.Env`, in a stable
   order, before the `CONDUIT_CONNECTOR_*` options (so the engine's own entries are last and win on
   duplicates).

The same option is applied at all three places the repository starts a plugin process, so discovery,
runtime and diagnostics see the same environment:

1. specification load when the plugin directory is scanned
   (`pkg/plugin/connector/standalone/registry.go`, `loadSpecifications`);
2. dispense for a running pipeline (same file);
3. `conduit doctor` (`cmd/conduit/root/doctor/doctorcheck/standalone.go`): `checkOne` re-execs the `conduit`
   binary, and `CheckConnectorPluginSpec` in the child builds the dispenser with its own
   `client.WithEnvVar` options. The doctor path must read the same `env-passthrough` setting; the
   re-exec'd child must receive it (for example through the inherited `CONDUIT_CONNECTORS_STANDALONE_ENV_PASSTHROUGH`
   environment variable or an explicit argument). How the setting reaches the child is an implementation
   detail to settle in the code PR; a doctor result that differs from runtime would be a bug.

### Allowlist

Matching is exact for names, prefix for `LC_*` and `XDG_*`, and case-insensitive on Windows. The table
lists what an allowlisted plugin receives.

| Group | Variables |
| --- | --- |
| Process basics | `PATH`, `HOME`, `USER`, `LOGNAME`, `TMPDIR`, `TEMP`, `TMP`, `TZ`, `XDG_*` |
| Locale | `LANG`, `LC_*` |
| Network | `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` and the lowercase forms |
| TLS roots | `SSL_CERT_FILE`, `SSL_CERT_DIR` |
| Windows | `SYSTEMROOT`, `SystemDrive`, `windir`, `USERPROFILE`, `USERNAME`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `ALLUSERSPROFILE`, `ProgramFiles`, `ProgramFiles(x86)`, `ProgramW6432`, `CommonProgramFiles`, `CommonProgramFiles(x86)`, `CommonProgramW6432`, `COMSPEC`, `PATHEXT`, `PROCESSOR_ARCHITECTURE`, `NUMBER_OF_PROCESSORS`, `OS`, `COMPUTERNAME`, `USERDOMAIN` |
| Go runtime | `GODEBUG`, `GOMAXPROCS`, `GOMEMLIMIT` |
| Conduit | `CONDUIT_CONNECTOR_*`, only the entries the engine itself sets via `WithEnvVar` |

Every `CONDUIT_*` variable from the host environment is excluded, including host-set `CONDUIT_CONNECTOR_*`
names. The exclusion always wins: operator prefix entries are matched only against names that survive it, so
`CONDUIT_` names are removed unconditionally, whatever the entry (for example `C*` or `COND*` never passes
`CONDUIT_ENGINE_TOKEN`). On Windows the exclusion is case-insensitive (`Conduit_x` is removed too).
The only variables carrying the `CONDUIT_CONNECTOR_` prefix that a plugin receives are the engine's
own entries, appended after the host-derived ones.

`HTTP_PROXY`/`HTTPS_PROXY` values can embed credentials (`http://user:pass@proxy:3128`). They are
allowlisted because many plugins cannot reach the network without them; operators who put credentials in
the URL should know the plugin receives them. Docs will say so.

Everything else, including cloud-provider variables, `*_TOKEN`, `*_SECRET`, `*_PASSWORD` and the engine's
own `CONDUIT_*` settings, is not passed unless named by the operator (host `CONDUIT_*` can never be named,
see _Config key_).

In `inherit` mode a host-set `CONDUIT_CONNECTOR_*` variable can override the engine's own entry of the same
name (existing behavior, unchanged by this document).

### Config key

`connectors.standalone.env-passthrough` (flag `--connectors.standalone.env-passthrough`, env
`CONDUIT_CONNECTORS_STANDALONE_ENV_PASSTHROUGH`, YAML under `connectors.standalone`):

| Value | Meaning |
| --- | --- |
| `inherit` | The full host environment. Escape hatch; logs a WARN at every start. |
| `allowlist` | `SkipHostEnv` plus the table above. The default from v0.21. |
| `allowlist:NAME1,NAME2,PREFIX_*` | The allowlist plus the named variables or prefixes, set engine-wide. |

Unknown values fail config validation with a stable error code, the config path and a suggestion listing
the valid values. `allowlist:*` (and any bare `*`) is rejected with the same kind of error: it is
`inherit` by another name, and one spelling for that is enough. A prefix must have a non-empty stem before
the `*`. Any name or prefix whose stem starts with `CONDUIT_` (`CONDUIT_*`, `CONDUIT_FOO`, `CONDUIT_CONN*`)
is also a config validation error with its own stable code: host `CONDUIT_*` is never passed, so accepting
such an entry would be silently ineffective. The setting is read at startup; changing it needs a restart
(same as `connectors.path`).

### Per-connector opt-in: not in v0.21

An engine-wide `allowlist:NAMES` is coarse: one pipeline's need for `AWS_REGION` becomes every plugin's
environment. A finer form would be a reserved connector setting such as
`conduit.env-passthrough: "AWS_REGION,AWS_PROFILE"`. It would live in pipeline config (a public contract),
so it needs its own versioned schema change, and it lets a pipeline author widen what a plugin receives,
which weakens the operator's boundary unless the engine-wide setting caps it. Decided: ship only the
engine-wide form now; add the per-connector form only if the first release shows real demand, with the
operator setting as the upper bound.

### Rollout

Decided: `allowlist` is the default in v0.21, with a loud `inherit` escape hatch. Operators who need the
full environment set `inherit`, and the engine logs a WARN at every start in that mode. The failure is loud
(a plugin fails to authenticate or connect at first use, with its own error) and nothing is persisted, so
rollback is a config change plus restart.

- v0.20.2 ships a startup warning listing environment variables that look like credentials and are passed to
  plugin processes. It lists names only, never values, and changes no behavior. It gives operators a release
  of notice before the default changes.
- v0.21 release notes announce the change and name the `inherit` workaround, with the one-line fix per
  failure in _Failure modes_.
- v0.21: default `allowlist`; `inherit` stays as the escape hatch. Windows keeps `inherit` as the default if
  the Windows CI job (see _Testing plan_) is not in place by the release.

## Alternatives considered

**A. Denylist of sensitive prefixes (`AWS_*`, `*_TOKEN`, ...).** Lost: a denylist fails open. Any secret
under a name nobody listed (`PGPASSWORD`, `MY_COMPANY_KEY`, a vendor token) is still passed, and the list
needs permanent maintenance. The reverse failure (a missing allowlist entry) is loud and fixable; a missing
denylist entry is silent. We do use sensitive prefixes, but only to make the v0.21 warning useful, not as
the control.

**B. Per-connector env declaration in the connector specification.** The plugin declares which variables
it needs and the engine passes exactly those. Lost: the declaration comes from the party we are restricting,
so it cannot be the boundary, and it needs a protocol/spec change and a rebuild of every plugin. It is
still reasonable as an input to a future operator-visible review ("this plugin asks for X"), not as the
enforcement.

**C. Do nothing, document it.** Lost: documentation alone leaves the default unchanged, so every plugin
keeps passing more than it needs. Documentation is still part of the change.

**D. Staged rollout (inherit by default for a release, flip later, remove `inherit` after two minors).**
Lost: it spends two releases keeping the wider default for a failure that is cheap to detect and fix. The
v0.20.2 warning gives the notice a stage would, without the extra releases.

**E. Sandboxing the plugin process (namespaces, seccomp, containers).** Not an alternative to env
restriction, a layer above it. Larger, platform-specific, and out of scope; this change is a prerequisite
for it being meaningful.

## Failure modes

Nothing here touches the record path: no acks, positions, ordering or serialized state change, so
invariants 1 to 7 are unaffected. The failures are start-up and configuration failures for plugins.

| Failure | Behavior | Mitigation |
| --- | --- | --- |
| Plugin needs cloud credentials from the environment (`AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`) | After the flip, plugin starts but auth fails at first connection; surfaces as the plugin's own error | Docs list `allowlist:NAMES`; preferred fix is putting credentials in connector config; the v0.20.2 warning names the variables in use beforehand |
| Plugin needs in-cluster discovery (`KUBERNETES_SERVICE_HOST`, `KUBERNETES_SERVICE_PORT`) | Plugin using the Kubernetes in-cluster client fails to find the API server | Name them via `allowlist:KUBERNETES_SERVICE_*`; documented |
| Plugin needs proxy or custom CA settings | Proxy and `SSL_CERT_*` are in the allowlist; other settings (`CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`) are not | Name them via `allowlist:`; revisit the allowlist from first-release feedback |
| Plugin is a script or wrapper needing `PATH`, `HOME`, interpreter vars | `PATH`/`HOME` pass; interpreter-specific variables (`JAVA_HOME`, `PYTHONPATH`) do not | Named via `allowlist:`; documented |
| Windows | Missing `SYSTEMROOT`/`windir` breaks Winsock and many runtimes; missing profile/shell variables break others | The Windows group is allowlisted and matched case-insensitively; see _Testing plan_ for the Windows job; Windows keeps `inherit` if that job slips |
| Locale / timezone | Wrong collation or time rendering if `LANG`/`LC_*`/`TZ` dropped | All allowlisted |
| Variable set but empty | Passed through as empty | n/a |
| Plugin env var collides with `CONDUIT_CONNECTOR_*` set by the engine | Engine entries are appended last and win | Test for duplicate keys |
| Specification load, dispense or `conduit doctor` run with different environments | Discovery or diagnostics succeed while runtime fails, or the reverse | One shared option applied at all three call sites; test asserts all three |
| Operator typos a name in `allowlist:` | Variable silently not passed | Startup log lists the resolved passthrough names; WARN for names not set in the engine's environment |
| Rollback after the flip | Operator sets `inherit` and restarts, no data migration | `inherit` stays available; no persisted state involved |
| Older engine reads a config that has the new key | Unknown-key behavior depends on the old binary; unverified | Check before release; document |

## Upgrade and rollback

- v0.20.2 adds the warning only. v0.21 adds the key and defaults to `allowlist`; release notes lead with it,
  with the one-line fix for each failure above.
- Rollback at any point is a config change plus restart. No pipeline config, connector state or position
  format changes, so there is no migration and no upgrade/downgrade state test beyond starting an N-1
  config on N+1.

## Observability

- Startup WARN in `inherit` mode (once per engine start, not per plugin): states that the full engine
  environment is passed to standalone plugins, gives the count of variables passed, and points at the config key.
  The v0.20.2 warning (which passes everything, as before) lists the names, never values, of variables that
  look like credentials (sensitive-looking prefixes and suffixes); this list is a diagnostic aid for the
  transition, not a control.
- Startup INFO in `allowlist` mode: the resolved list of operator-added names.
- Metric `conduit_standalone_plugin_env_vars_passed{mode}` (gauge, set at plugin start) so fleets can see
  which engines pass the full environment.
- Each start logs the number of variables passed to the plugin process at debug level.
- Runbook entry under `docs/operations/`: symptom "standalone plugin fails to authenticate or connect
  after upgrade", diagnosis (compare plugin docs with the resolved passthrough list), remediation
  (`allowlist:NAMES` or move the setting into connector config).

## Relationship to PR #3041

The built-in connector credential policy in PR #3041 covers built-in connectors only. Standalone plugins
are out of scope there; this document narrows what an allowlisted plugin receives and is not a sandbox.

## Testing plan

- Unit: allowlist resolution, `LC_*`/prefix matching, case-insensitive matching on Windows, duplicate
  keys, mode parsing and error codes (including rejection of `allowlist:*`).
- Integration: a test plugin that dumps its environment. The test sets a sentinel secret in the engine's
  environment under a non-`CONDUIT_` name (for example `SENTINEL_SECRET_X`; every host `CONDUIT_*` is
  excluded anyway, so a `CONDUIT_` sentinel would prove nothing) and asserts it is absent from the plugin's
  environment in all three spawn paths (specification load, dispense, `conduit doctor`) under `allowlist`,
  and present only where explicitly named via `allowlist:NAME`. Also assert the exact variable set in
  `inherit`, `allowlist` and `allowlist:NAMES`, and that no host `CONDUIT_*` variable appears.
- Windows: the repository has no Windows runner. Filter logic, including case-insensitive matching, is
  tested as pure functions on Linux. In addition, a `windows-latest` job spawns a test plugin that makes
  a TCP connection under `allowlist`; it is non-required at first. If it slips, Windows keeps `inherit` as
  the default until the job exists and passes. Also test rejection of `CONDUIT_`-stem entries in
  `allowlist:`.
- `CONDUIT_` exclusion: with operator entries `C*`, `COND*`, `CONDUIT_*` (the last rejected at validation)
  and a host `CONDUIT_X` set, assert `CONDUIT_X` never reaches the plugin; on Windows, also `Conduit_x`
  (pure-function test on Linux, real spawn in the Windows job).
- Docs: `conduit.io` configuration reference, `llms.txt`, and the migration note, updated together.

## Consequences

- Plugin processes receive a fixed, documented set of variables plus operator-named ones by default. This is
  not a sandbox; a same-user plugin can reach other sources of the same data.
- Some existing plugin deployments need a one-line config change at the default flip.
- The sandboxing work (Alternative E) remains the layer that would make this a boundary.

## Related

- `docs/design-documents/20261012-builtin-connector-ambient-credentials.md` (PR #3041)
- `go-plugin` `ClientConfig.SkipHostEnv`
- Roadmap: v0.21 connector-certification
