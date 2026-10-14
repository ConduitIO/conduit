# Restricting the environment of standalone connector plugins

## Summary

Least privilege: a standalone connector plugin should receive the environment it needs to run, and
nothing more. This change has the engine pass a fixed allowlist of variables (plus any the operator
names) to plugin processes instead of the engine's whole environment. It reduces what a plugin receives
by default. It is not a sandbox; a same-user plugin can reach other sources of the same data.

Operators control it with `connectors.standalone.env-passthrough`. The same filtered environment applies
to every place the engine starts a plugin process (see _Mechanism_). No `conduit-connector-protocol`
change and no SDK change. `allowlist` becomes the default in v0.21 only if v0.20.2 with the warning has
shipped first; see _Rollout_.

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
| go-plugin handshake | Always set by `go-plugin` itself, after the engine's entries, whatever the mode: the magic cookie `CONDUIT_PLUGIN_MAGIC_COOKIE` (key and value from `conduit-connector-protocol` `pconnector.HandshakeConfig`), `PLUGIN_MIN_PORT` and `PLUGIN_MAX_PORT` (`10000` and `25000`, go-plugin's defaults, because the protocol client sets no ports), `PLUGIN_PROTOCOL_VERSIONS` (the protocol versions the client offers, `1` and `2`; the order is not stable) |
| go-plugin, only when enabled | `PLUGIN_CLIENT_CERT` with `AutoMTLS`; `PLUGIN_MULTIPLEX_GRPC=true` with `GRPCBrokerMultiplex`; `PLUGIN_UNIX_SOCKET_GROUP` with a `UnixSocketConfig` group; `PLUGIN_UNIX_SOCKET_DIR` with a `RunnerFunc`. The protocol client (v0.10.0) enables none of these, so a plugin receives none of them today |

The two go-plugin rows are read from `go-plugin` v1.8.0 `client.go` (`Client.Start`, the `env` slice and the
appends after it) and `constants.go`, and from `conduit-connector-protocol` v0.10.0 `pconnector/client`. go-plugin
appends them to `Cmd.Env` after the host environment (or after nothing, with `SkipHostEnv`) and after the engine's
own entries, so they are present in every mode and win over any host variable of the same name (`os/exec` keeps the
last value of a duplicate). They are not secrets: the cookie is a constant compiled into every plugin, and it only
tells a plugin binary that it was started by a host. `CONDUIT_PLUGIN_MAGIC_COOKIE` is the one `CONDUIT_` name a plugin
always receives that is not a `CONDUIT_CONNECTOR_*` entry; the host-`CONDUIT_*` exclusion below does not remove it,
because go-plugin adds it after the filter runs.

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

Unknown values fail config validation with `plugin.standalone_env_passthrough_invalid`, the config path
`connectors.standalone.env-passthrough` and a suggestion listing the valid values. `allowlist:*` (and any bare
`*`) is rejected with the same code: it is `inherit` by another name, and one spelling for that is enough. A
prefix must have a non-empty stem before the `*` (same code). Any name or prefix whose stem starts with
`CONDUIT_` (`CONDUIT_*`, `CONDUIT_FOO`, `CONDUIT_CONN*`) fails with its own code,
`plugin.standalone_env_passthrough_conduit_name`, naming the entry: host `CONDUIT_*` is never passed, so
accepting such an entry would be silently ineffective.

Stable codes this change adds:

| Code | Kind | When |
| --- | --- | --- |
| `plugin.standalone_env_passthrough_invalid` | config validation error | Unknown mode, `*` or `allowlist:*`, empty prefix stem, empty name in the list |
| `plugin.standalone_env_passthrough_conduit_name` | config validation error | An `allowlist:` entry whose stem starts with `CONDUIT_` |
| `plugin.standalone_env_inherit` | WARN, once per engine start | Mode is `inherit` |
| `plugin.standalone_env_name_unset` | WARN, once per engine start | A name in `allowlist:` is not set in the engine's environment (likely a typo) |

`plugin.standalone_env_passed` (the v0.20.2 warning, #3045) keeps its code and meaning.

The setting is read at startup; changing it needs a restart
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

Hard gate, Decided by DeVaris 2026-10-14: the default flips to `allowlist` in v0.21 only if v0.20.2, with
the `plugin.standalone_env_passed` warning, has shipped first. "Shipped" means the v0.20.2 tag and release
exist before the v0.21 release is cut.

The rule behind the gate, so the flip cannot stall: `allowlist` becomes the default in the minor release
after the first release that carries the warning.

- If v0.20.2 ships with the warning before the v0.21 cut, v0.20.2 is that first release, and v0.21 flips the
  default.
- If it does not, v0.21 is the first release that carries the warning: it ships the key with `inherit` as
  the default, the `plugin.standalone_env_passed` warning and the `inherit` WARN. v0.22 flips the default,
  whatever happens to v0.20.x afterwards. A later v0.20.x patch does not move the flip again.

Status on 2026-10-14: not met yet. The warning is merged on `main` (#3045); the backport to
`release/v0.20.x` (#3046) is open, and no v0.20.1 or v0.20.2 tag exists. Because the release that flips is
not fixed yet, the shipped warning text says "a future release will restrict", not "v0.21 will restrict"
(see the warning bullet below).

Once the default flips: `allowlist` is the default, with a loud `inherit` escape hatch. Operators who need the
full environment set `inherit`, and the engine logs a WARN at every start in that mode. The failure is loud
(a plugin fails to authenticate or connect at first use, with its own error) and nothing is persisted, so
rollback is a config change plus restart.

- v0.20.2 ships a startup warning listing environment variables that look like credentials and are passed to
  plugin processes. It lists names only, never values, and changes no behavior. It gives operators a release
  of notice before the default changes. Its text must not tell operators to set `env-passthrough`: v0.20.x
  does not know the key (see _Upgrade and rollback_), so the suggestion would be a no-op on YAML or the
  environment variable and a start failure as a flag. Checked on `main` 2026-10-14: the warning message
  (`pkg/plugin/connector/standalone/envwarn.go`), the doctor finding and its suggestion
  (`cmd/conduit/root/doctor/doctorcheck/standalone_env.go`) and `docs/operations/standalone-plugin-environment.md`
  suggest trusted plugins, a minimal environment and `env-warning: false`, and none suggests setting
  `allowlist`. They do say "v0.21 will restrict the plugin environment", which the rule above can make wrong.
  #3065 changes that to "a future release will restrict" on `main`; it must merge before the v0.20.2
  backport is cut, and the backport (#3046) takes the same text.
- v0.21 release notes announce the change and name the `inherit` workaround, with the one-line fix per
  failure in _Failure modes_.
- v0.21: default `allowlist`; `inherit` stays as the escape hatch. Windows keeps `inherit` as the default if
  the Windows CI job (see _Testing plan_) is not in place by the release.

## Alternatives considered

**A. Denylist of sensitive prefixes (`AWS_*`, `*_TOKEN`, ...).** Lost: a denylist fails open. Any secret
under a name nobody listed (`PGPASSWORD`, `MY_COMPANY_KEY`, a vendor token) is still passed, and the list
needs permanent maintenance. The reverse failure (a missing allowlist entry) is loud and fixable; a missing
denylist entry is silent. We do use sensitive prefixes, but only to make the v0.20.2 warning useful, not as
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
| Older engine (v0.20.x) started with the new setting | YAML key and environment variable: silently ignored, the plugin gets the full environment. Flag: `Error: unknown flag: --connectors.standalone.env-passthrough`, exit 1, the engine does not start (verified on the v0.20.0 binary) | Release note and _Upgrade and rollback_ say to remove the flag before a downgrade |

## Upgrade and rollback

- v0.20.2 adds the warning only. v0.21 adds the key and, if the gate in _Rollout_ is met, defaults to
  `allowlist` (otherwise v0.22 does); release notes lead with it, with the one-line fix for each failure above.
- Rollback on v0.21 is a config change plus restart (`inherit`). No pipeline config, connector state or
  position format changes, so there is no migration and no upgrade/downgrade state test beyond starting an
  N-1 config on N+1.
- Downgrade to v0.20.x, verified 2026-10-14 against the released v0.20.0 binary (darwin/arm64), started with
  a fake environment (`env -i`):
  - YAML `connectors.standalone.env-passthrough: allowlist`: ignored without a warning or error; the engine
    starts normally. v0.20.x has no filter, so plugins get the full host environment (that part is read from
    the code; the run had no plugin installed).
  - Environment variable `CONDUIT_CONNECTORS_STANDALONE_ENV_PASSTHROUGH=allowlist`: ignored the same way.
  - Flag `--connectors.standalone.env-passthrough=allowlist`: `Error: unknown flag:
    --connectors.standalone.env-passthrough`, exit status 1. The engine does not start. A systemd unit, a
    container command or a Helm value that passes the flag stops working on downgrade.
  - So a downgrade silently widens the plugin environment back to everything (YAML, env var) or fails to
    start (flag). Neither loses data. Before downgrading, remove the flag; expect the full environment on
    v0.20.x whichever form was used.
- Setting `allowlist` on v0.20.x does nothing, for the same reason. The v0.20.2 warning and its docs must not
  suggest it (checked, see _Rollout_).

Release-note text for v0.21 (draft):

> Standalone connector plugins now receive a restricted environment by default
> (`connectors.standalone.env-passthrough: allowlist`): a fixed list of system variables plus the ones you
> name with `allowlist:NAME1,NAME2`. Set `inherit` to keep the previous behavior. If you downgrade to
> v0.20.x, remove `--connectors.standalone.env-passthrough` from your command line first: v0.20.x rejects
> the flag and will not start. v0.20.x ignores the YAML key and the environment variable, so after a
> downgrade plugins again receive the full environment.

If the gate is not met, the v0.21 note's first sentence becomes "Conduit adds
`connectors.standalone.env-passthrough`; the default stays `inherit` in this release and becomes `allowlist`
in v0.22", and the text above moves to the v0.22 notes. The downgrade paragraph stays in both.

## Observability

- Startup WARN in `inherit` mode (once per engine start, not per plugin): states that the full engine
  environment is passed to standalone plugins, gives the count of variables passed, and points at the config key.
  The v0.20.2 warning (which passes everything, as before) lists the names, never values, of variables that
  look like credentials (sensitive-looking prefixes and suffixes); this list is a diagnostic aid for the
  transition, not a control.
- Startup INFO in `allowlist` mode: the resolved list of operator-added names.
- Metric `conduit_standalone_plugin_env_vars_passed{mode}` (gauge) so fleets can see which engines pass the
  full environment. Semantics with several plugins: the mode is engine-wide (read once at startup), so the
  gauge has exactly one series per engine, labelled with the configured mode (`inherit`, `allowlist`, or
  `allowlist_named` for `allowlist:NAMES`; the names themselves are never a label). Its value is the number
  of host-derived variables the filter passes. That number is the same for every plugin the engine starts,
  because both inputs (the engine's environment and the setting) are fixed at startup, so it is set once at
  startup, not per plugin start, and does not depend on how many plugins run. It excludes the engine's
  per-plugin `CONDUIT_CONNECTOR_*` entries and go-plugin's handshake variables. There is no per-plugin
  label. `conduit doctor` runs in its own process and exports no metrics.
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

## Decisions

- Hard gate for the default flip. Decided by DeVaris 2026-10-14: the default becomes `allowlist` in v0.21
  only if v0.20.2 with the warning has shipped first. See _Rollout_.
- Fallback (round-2 review, 2026-10-14): the default becomes `allowlist` in the minor release after the first
  release that carries the warning: v0.21 if that is v0.20.2, v0.22 if it is v0.21. Confirmed by DeVaris
  2026-10-14.

## Related

- `docs/design-documents/20261012-builtin-connector-ambient-credentials.md` (PR #3041)
- `go-plugin` `ClientConfig.SkipHostEnv`
- Roadmap: v0.21 connector-certification
