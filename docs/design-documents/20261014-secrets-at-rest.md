# Secrets at rest: store `${VAR}` references, not expanded values

## Summary

Conduit expands `${VAR}` while it parses a pipeline config file and stores the expanded value. The
secret ends up in plaintext in badger, SQLite or Postgres and in every backup of them (#2988). The
API redacts it on output (#2926); the store does not.

This doc proposes, for v0.21:

- A setting that came from a config file and contains an environment reference is stored as its
  **template** (`postgres://app:${DB_PASSWORD}@db/app`), in a new field next to `Settings`. The value
  is never written to the database.
- The template is resolved from the engine's environment at the points where a plugin is
  configured (connector start, processor open, DLQ open, lifecycle events), into a transient map
  that is never assigned back to a stored entity.
- Databases written by v0.20 and earlier migrate on the first v0.21 start, through the config
  reconcile that already runs on every start, inside the existing per-pipeline transaction.

It does not encrypt anything and adds no key management. Secrets typed literally through the API
stay as they are today; that is a stated residual, not an oversight (see _Non-goals_ and
_Alternatives_). It is the first step of the v0.22 external secret stores item: the stored
template is the seam a Vault or KMS provider plugs into, and nothing in this design builds that.

Risk tier: **Tier 1** (stored-state format change, config handling, a migration of existing
databases). Human sign-off required. This PR is docs only.

Roadmap item: v0.21: Secrets at rest (#3018, fixes #2988). Tracking issue #3002.

## Context

### What happens today

- `pkg/provisioning/config/yaml/hooks.go` (`envDecoderHook`) runs `os.ExpandEnv` on **every** string
  scalar the YAML decoder visits, wired in by `parseConfiguration` in
  `pkg/provisioning/config/yaml/parser.go`. The parser output therefore has no memory of the
  reference.
- Provisioning creates entities from that output through `pkg/provisioning/import_actions.go`
  (`createConnectorAction`, `updateConnectorAction`, and the processor and DLQ equivalents), which
  call `connector.Service`, `processor.Service` and `pipeline.Service`.
- The stores write into `conduit-commons/database` (badger, SQLite, Postgres, in-memory), under
  `connector:instance:<id>` and so on. They do not all encode the same way:
  - `pkg/processor/store.go` and `pkg/pipeline/store.go` JSON-encode the whole `Instance`.
  - `pkg/connector/store.go` does **not**. `PrepareSet` copies the instance into a new
    `Instance` field by field, and copies `Config` as `Config{Name, Settings}` only. A new `Config`
    field is silently dropped by it, which would leave neither the value nor the template. The
    persister reaches this path on every flush (`pkg/connector/persister.go`, `p.store.PrepareSet`),
    so it is also the path that decides what survives a position flush.
- Where secrets sit in a stored record:
  - `connector.Instance.Config.Settings`
  - `connector.Instance.LastActiveConfig.Settings`, a second full copy, written when a connector
    starts (`pkg/connector/source.go`, `destination.go`)
  - `processor.Instance.Config.Settings`
  - `pipeline.Instance.DLQ.Settings`
- `connector.Persister` (`pkg/connector/persister.go`) rewrites the connector record, config included,
  through `PrepareSet` on every position flush. Anything that ever lands in `Instance.Config` is
  persisted again within seconds of a pipeline running. `PrepareSet` copies `Config` and
  `LastActiveConfig` but keeps the same `Settings` map pointers, so an in-place mutation of a
  `Settings` map by any holder is encoded at the next flush.
- `restoreRedactedSettings` (`pkg/orchestrator/redacted.go`) and `redactSettings`
  (`pkg/http/api/toproto/redact.go`) implement the `"***"` contract. Every Settings value in every
  API response is `"***"`, because no per-parameter sensitivity exists: `conduit-commons` v0.8.0 and
  `conduit-connector-protocol` v0.10.0 have no `sensitive` or secret marker on `config.Parameter`
  (checked by search; the same gap is noted in `pkg/foundation/log/redact.go`, tracked as #2566).

### Two facts that shape the design

1. **File-provisioned entities are re-derived from the file on every start.**
   `provisioning.Service.Init` (`pkg/provisioning/service.go`) runs before `lifecycleService.Init`
   (`pkg/conduit/runtime.go`), re-parses the files, exports the stored config
   (`pkg/provisioning/export.go`), diffs, and applies updates. The environment is therefore already
   required at every start. Today, if `${DB_PASSWORD}` is unset at a restart, the stored good value
   is silently overwritten with `""` (`os.ExpandEnv` expands an unset variable to empty).
   Storing the DB copy of such a setting adds no new dependency; it removes a copy.
2. **The API cannot be trusted to carry references.** The API is unauthenticated by default
   (`redacted.go` says so). If a client could submit `${AWS_SECRET_ACCESS_KEY}` as a setting and the
   engine resolved it, anyone who can reach the API could point a connector at a host they control
   and read the engine's environment. References are therefore only honored when they come from a
   file the engine itself parsed, which is operator-controlled.

### Entities created through other doors

`ApplyPipeline` and `PlanPipeline` (`pkg/http/api/pipeline_v1.go`) and the `conduit pipelines
deploy|apply` CLI (`cmd/conduit/internal/deploy/deploy.go`) take a document whose settings are
already expanded (the CLI expands in its own process), and import it as
`pipeline.ProvisionTypeConfig` (`pkg/provisioning/plan.go`). `ProvisionedBy` therefore does **not**
distinguish "came from a file in the engine's environment" from "came over the wire". The
distinction this design needs is provenance of the reference, and it is carried by the new field,
not by `ProvisionedBy`.

`ExportPipeline` is not implemented (`pipeline_v1.go` returns `ErrNotImpl`), and there is no
`conduit pipelines export` command. `provisioning.Service.Export` is internal, used only to compute
the diff. The brief mentions `pipelines export`; there is nothing to change today, and a constraint
for whoever builds it (see _Interactions_).

## Threat model

**Assets.** Credentials that operators pass to connectors, processors and the DLQ through
`${VAR}`: database passwords, cloud keys, API tokens, connection URLs with embedded passwords.

**Adversaries and what this design does.**

| Adversary | Capability | After this design |
| --- | --- | --- |
| Reads a stolen or leaked copy of the DB files or a backup (badger directory, SQLite file, `pg_dump`, volume snapshot, replica) | Offline read of persisted state | Protected for `${VAR}` secrets written after the migration. The value is not in the database. Not protected for data written before it (see _Residue_). |
| Reads the live database without host access (Postgres role with `SELECT` on the Conduit table, shared DB host) | Online read of persisted state | Same as above. |
| Reads the API | Network access to an unauthenticated API | Unchanged: already `"***"` (#2926). New: the API cannot be used to resolve references (fact 2). |
| Has the engine's environment or process memory (same OS user, `/proc/<pid>/environ`, a debugger, a standalone plugin, which inherits the host environment: `docs/operations/standalone-plugin-environment.md`) | Reads the secret regardless of storage | **Out of scope.** No at-rest design helps. Plugin environment restriction is tracked separately for v0.21. |
| Has write access to the pipeline config files | Can reference any env var in a setting | Out of scope. They already can today; the file is the operator's trust boundary. This holds only if the operator read what is in the directory: registry templates written into the pipelines directory are parsed with the engine's environment too. |

**What this does not protect.**

- Secrets typed literally into API-created entities (`POST /v1/connectors` with a password in
  `config.settings`), and settings expanded client-side by `pipelines deploy|apply` or sent through
  `ApplyPipeline`. They are stored as given. DB-level protection (disk encryption, Postgres TDE,
  restricted roles) is the control for these, and the runbook says so.
- Secrets written as a literal in the YAML file. The file already holds them.
- **Mixed templates store their literal part in the clear.** A template is stored verbatim, so
  `postgres://app:hunter2@${HOST}/db`, or a literal that happens to contain a `$` reference,
  persists `hunter2` under `SettingRefs`. Only the referenced part is kept out of the database.
  The runbook says to put the whole secret behind the variable (`${DB_URL}`). Optional: a linter
  warning for credential-looking keys whose value mixes a literal with a reference.
- Connector state: positions and offsets are not credentials, and are not touched.
- Secrets in logs and plugin errors. Log redaction (`pkg/foundation/log/redact.go`) covers settings;
  a plugin that echoes its config into an error is outside this design.

## Constraints

- Data-integrity invariants (CLAUDE.md). The ones this design touches:
  - **Invariant 5** (atomic state writes): migration and every store write use the store's
    transactional API. No new write path bypasses it.
  - **Invariant 2** (positions crash-safe): `Instance.State` must survive the migration untouched.
  - **Invariant 7** (graceful shutdown / `kill -9` recoverable): a crash at any instant of the
    migration leaves a database the next start can finish migrating.
- Backward compatibility: the stored format must be readable by N+1, and the change must not
  strand a downgrade to v0.20.x.
- API contract: `"***"` redaction and the keep-stored-value rules from #2926 are preserved, with no
  proto change.
- No speculative generality: no provider interface, no key hierarchy, no plugin API change.
- No new behavior for plugins: they still receive a plain `map[string]string` at `Configure`.

## Decision

### 1. Stored form

`Config` on connectors, processors and the DLQ gains one optional field:

```go
// SettingRefs holds settings whose value comes from the engine's environment,
// as the original template text. A key is in Settings or in SettingRefs, never
// both.
SettingRefs map[string]string `json:",omitempty"`
```

Stored JSON gains `"SettingRefs":{"aws.secretAccessKey":"${AWS_SECRET}"}` and loses the key from
`"Settings"`. `omitempty` keeps records with no references byte-identical to v0.20, which keeps
the golden store tests (`pkg/connector`, `pkg/processor`, `pkg/pipeline` `testdata`) unchanged for
them. Nothing changes in key prefixes (`connector:instance:`, `processor:instance:`,
`pipeline:instance:`), so no store migration function is needed in the `migratePre041` style.

The field is added to three distinct structs: `connector.Config`, `processor.Config`
(`pkg/processor/instance.go`) and `pipeline.DLQ` (`pkg/pipeline/instance.go`, which is its own
struct and not a `Config`). **`connector.Store.PrepareSet` must copy it explicitly**, in both
`Config` and `LastActiveConfig` (`pkg/connector/store.go`); the other two stores encode their
instance whole and need no store change. `omitempty` means the golden tests cannot catch a
dropped field, so a dedicated test is required (see _Tests_).

`LastActiveConfig` is a `Config` and carries `SettingRefs` the same way. It must never hold a
resolved value (see _Migration_ for the existing plaintext in it).

The template is the original scalar text, unmodified. A value **is a reference** exactly when
`os.Expand`, given that text, invokes its mapping function at least once. This reuses today's
grammar (`$VAR`, `${VAR}`, and Go's other quirks) so no existing config changes meaning, and it is
testable: for every input, `Resolve(template)` must equal what `os.ExpandEnv` returned in v0.20
(a fuzz target, see _Tests_). Text with `$` that `os.Expand` does not treat as a name stays a
plain value.

### 2. Which fields

Every reference in a **settings value** of a connector, processor or DLQ is stored as a reference.
Not only "secret" ones. Reasons:

- There is no sensitivity metadata to select with (see _Context_). A key-name heuristic leaks
  exactly the key it fails to match; `redact.go` made the same call and chose deny-by-default.
- A reference is the operator's own statement that the value lives outside the file. Treating it as
  not stored is correct for non-secrets too (`${PORT}`, `${BUCKET}`).
- Ids, names, `plugin`, `type`, `status`, descriptions and the pipeline `id` keep today's behavior:
  expanded at parse time, stored expanded. Operators must not put secrets there. The config linter
  (`pkg/provisioning/config/yaml/linter.go`) already sees these nodes and can warn (see
  _Observability_).

When the `conduit-commons` `sensitive` marker lands (#2566), it can drive redaction precision and the
doctor hint. It does not change storage: storing every reference as a reference stays correct.

### 3. Where references are parsed

The parser gains an option, `KeepSettingRefs`, used only by `provisioning.Service` and the dev
watcher (`pkg/conduit/dev/apply.go`), which parse in the engine's own environment. With it, a
reference in a settings value goes to `SettingRefs` and is absent from `Settings`. Without it (the
default, used by `validate`, `lint`, `generate`, `pipelines deploy|apply` and the plugin uninstall
commands) behavior is exactly today's: expanded into `Settings`, `SettingRefs` nil. One
representation per mode; a caller cannot hold both a template and its value for the same key.

`path []string` in `envDecoderHook` already identifies where a scalar sits, so the hook can tell a
settings value from a structural field.

**Who may populate `SettingRefs`.** Only the parser in `KeepSettingRefs` mode. That is a rule, and
the design backs it with checks, because a convention is not a trust boundary:

- `fromproto` conversions (`pkg/http/api/fromproto`) have no `SettingRefs` to fill, and a test
  walks every API entry point (`CreateConnector`, `UpdateConnector`, processor equivalents,
  `PlanPipeline`, `ApplyPipeline`) asserting a stored entity never has `SettingRefs` set by a request.
- **The compiler enforces who can set `SettingRefs`, not a runtime caller check.** The field's type
  is an opaque `parsed.Refs` defined in `pkg/provisioning/internal/parsed`, with unexported fields
  and a single constructor in that package. Go's `internal` rule means only code under
  `pkg/provisioning/` can import it; in practice that is the YAML parser
  (`pkg/provisioning/config/yaml`). Everything else, including `fromproto`, the API handlers,
  `Plan`, `ApplyPlan`, `ApplyPlanLive` and `pkg/conduit/dev`, can pass a `parsed.Refs` along or
  copy it but cannot build a non-zero one, and the zero value means "no references". A request
  converted by `fromproto` therefore carries no references by construction. `Plan`, `ApplyPlan` and
  `ApplyPlanLive` need no check. The residual is code inside `pkg/provisioning/` itself, which is
  the layer that is allowed to; review of that package is the control. Tests still assert the
  behavior end to end.
- `ApplyPipeline` onto a pipeline that was provisioned from a file overwrites its references with
  the literal values in the request (the request carries literals only, D3). Behavior chosen:
  **allowed, as today**, and the reconcile at the next start restores the references from the
  file. Between the two the DB holds the literals the caller sent. The runbook says so. Refusing it
  would change a documented v0.20 behavior for a case that already exposes the secret to the caller.

`config.Connector`, `config.Processor` and `config.DLQ` (`pkg/provisioning/config/parser.go`) gain
`SettingRefs`. `Enrich` and `Validate` neither read nor need values. The
plan diff (`pkg/provisioning/plan.go`) compares `SettingRefs` as templates; `Change.ConfigPaths`
already names fields without values and keeps doing so (`settings.<key>` for both). The plan hash
covers templates, never values, so a rotated secret does not make a plan "stale".

### 4. Where references are resolved

A small package, `pkg/secretref` (name open), with one exported function and one error code:

```go
// Resolve returns settings merged with every reference in refs resolved from
// the process environment. The result is for a plugin call only.
func Resolve(settings, refs map[string]string) (map[string]string, error)
```

No interface, no registry. One implementation (environment). v0.22 adds providers behind this
function with its own design doc.

Call sites. Resolution happens as late as possible, inside the function that builds the plugin
request, so the Instance never holds a resolved map:

| Site | File |
| --- | --- |
| source configure | `pkg/connector/source.go` (`configure`) |
| destination configure | `pkg/connector/destination.go` (`configure`) |
| lifecycle event payloads | `pkg/connector/source.go`, `destination.go` (`triggerLifecycleEvent`) |
| processor open and egress policy | `pkg/processor/runnable_processor.go` (`Open`), `pkg/processor/service.go` (`PolicyFromSettings`) |
| live processor reconfigure | `provisioning` `applyInPlace` -> `lifecycle.ReconfigureProcessor`, and `processor.Service.UpdateWhileRunning` |

**The DLQ is not a resolve site.** `buildDLQHandlerNode` (`pkg/lifecycle/service.go`, and
`pkg/lifecycle-poc/service.go`) builds a destination `connector.Instance` through
`connectors.Create` with `pl.DLQ.Settings`. If it resolved first, the resolved values would be
assigned into an Instance. That is safe today only because `ProvisionTypeDLQ` skips the store,
which is an accident of the provision type. Instead it passes `{Settings, SettingRefs}` through
`Create`, and `Destination.configure` resolves like any other destination. `batching_guard.go`
reads `DLQ.Settings` for `sdk.batch.*` keys, which a reference may also supply; it must treat a
key present in `SettingRefs` as present-but-unresolved and resolve for the check (a transient
local, never stored).

**Invariant R1.** A resolved map is a local variable of a plugin call. It is never assigned to
`Instance.Config`, `Instance.LastActiveConfig`, any `Instance` field, a logger field or an error
message. Enforcement: the result of `Resolve` has a distinct named type whose `String`, `GoString`
and `MarshalJSON` print `"***"` for every value, so an accidental `Any(...)` or `json.Marshal`
cannot leak it (the plugin boundary converts with an explicit `map[string]string(...)`). A test
enforces R1 end to end: see _Tests_, canary scan. Each site carries
`// Invariant R1: resolved settings are transient, never stored` at the point of use.

The persister is the reason this must be enforced by a scan and not by review: it re-serializes
`Instance.Config` on every position flush, so a single assignment bug would write the secret within
seconds of the pipeline running.

No holder may mutate a `Settings` or `SettingRefs` map in place: the maps are shared pointers and
are encoded by `PrepareSet` at flush time. Updates replace the map. A test (race detector on) pins
this for the connector and processor services.

**"Started" and "equal" are defined over Settings plus SettingRefs.** Three places read
`Settings` today in a way that a references-only connector would break:

- `LastActiveConfig.Settings == nil` means "never started" (`source.go` `OnDelete`, `destination.go`
  `OnDelete`). An all-reference connector has an **empty** `Settings` and a populated `SettingRefs`.
  New definition: never started iff both are nil. Otherwise `OnDelete` would skip the delete event
  (for example a replication-slot drop) for exactly the connectors this change affects.
- `isEqual` (`source.go`, and the destination equivalent) ends in a nil comparison, so a nil
  versus empty `Settings` already counts as a change. It becomes a comparison of
  `(Settings, SettingRefs)` pairs, nil-ness preserved per map, comparing **templates**.
- `triggerLifecycleEvent` uses the same maps for the compare and for the payload. It splits them:
  compare on templates, resolve for the payload. The payload of an update event therefore carries
  **current-environment** values for both `ConfigBefore` and `ConfigAfter` of a reference-backed
  key; the plugin cannot see the previous value of a rotated secret. The release notes and the
  runbook state this.

A test is required for an all-references connector across migrate, restart and delete.

Resolution is at the point of use, so the value is read from the environment at connector open, not
at provisioning. Consequences, stated rather than hidden:

- A pipeline stop and start inside one engine process reads the same process environment. Secret
  rotation means changing the environment and restarting the engine, as it does today for file
  provisioning (the reconcile that picks up a new value also runs at start).
- A changed environment value is **not** a config change. Today it is: the reconcile diff sees a
  new stored value, issues an update, and the connector receives a `LifecycleOnUpdated` event with
  old and new config. With references the templates are equal, no update is issued, and no event
  fires. For credentials this is the desired behavior; for a connector that reacts to, say, a
  changed table name given as `${TABLE}`, the operator must change something stored (or restart the
  pipeline after a deliberate edit). This is a behavior change for release notes.
- `LifecycleOnDeleted` receives the old config. Resolved against the current environment, it gets
  current credentials, which is better than possibly revoked old ones.

### 5. Missing and empty variables

`os.ExpandEnv` cannot tell unset from empty. `Resolve` uses `os.LookupEnv`:

- **Set, including set to the empty string:** value used as is (explicit empty is deliberate).
- **Unset:** a coded error, `secretref.env_unset` (`codes.FailedPrecondition`), naming the variable,
  the entity ID and the settings key as a config path, with the suggestion "export the variable in
  the engine's environment, or set it to the empty string if an empty value is intended". It never
  echoes any value.

Rollout, to respect the deprecation policy (announce, warn, remove across at least two minor
versions):

- v0.21: an unset variable resolves to `""`, exactly as today, with a warning carrying the code. The
  warning is emitted **per pipeline start** (not once per process), so a pipeline restarted later
  warns again. It is also surfaced in the degraded cause when the pipeline fails for another
  reason, in `pipelines status` (a `warnings` entry with the code and the variable name), and in
  `conduit doctor`. `--secrets.strict-env` (and its config/env equivalents) turns the warning into
  the error: the pipeline fails to start as degraded with the coded cause
  (`docs/design-documents/20260706-degraded-pipeline-error-cause.md`) and no plugin sees a
  partial config.
- v0.22: the warning is repeated in the release notes and `conduit doctor` reports unset
  variables as a failure.
- v0.23: strict is the default; `--secrets.strict-env=false` is the escape hatch.

Exceptions to the warn-only default, in v0.21:

- **`sdk.egress.*` keys hard-fail on an unset variable regardless of the flag.** They are read on
  every processor open (`pkg/processor/service.go`, `PolicyFromSettings`), and an empty value there
  widens or blanks an egress policy. Fail closed.
- **Strict mode and deletion.** A resolve failure while delivering `OnDelete` or a delete event
  must not be silent: today's v0.20 path would deliver the event with whatever expanded, but a
  strict failure would drop it and, for example, never drop the replication slot. "Retry at the next
  start" is not implementable: `connector.Service.Delete` removes the record from the store and
  memory first, and only then `instance.Close` calls `OnDelete`, whose error is only logged
  (`pkg/connector/service.go`, `pkg/connector/instance.go`). By the time `OnDelete` fails nothing
  is left to retry, and `deleteOldPipelines` (`pkg/provisioning/service.go`) retries only when the
  delete action returns an error. Behavior, a preflight **before** the store delete:
  1. In strict mode, before `store.Delete`, resolve the `LastActiveConfig` payload that the
     delete event will carry. If an unset variable would fail it, the delete action returns the
     coded `secretref.env_unset` error. The record survives and the existing `deleteOldPipelines`
     retry works.
  2. The preflight covers **every** connector of the pipeline before the first delete action, so a
     pipeline cannot end up half-deleted.
  3. There is no force flag. The escape hatch is the variable itself: set it to the real value, or
     to the empty string to accept a delete event that will probably fail. The error message says
     this and names what may be orphaned (for a Postgres source, the replication slot).
  4. Non-strict behavior is unchanged from v0.20.
  5. Provisioning deletes are not one database transaction: `Service.Delete` runs the delete
     actions and, on a later failure, its rollback actions re-create what was removed. A delete
     event may already have been delivered when that happens, so a retry redelivers it. Delete
     events are idempotent today, and this design relies on it.
  6. A "pending delete" record that survives the connector was rejected: it adds new persisted
     state and a crash-recovery surface (Invariant 5) for a corner case.

  The underlying ordering problem is not secrets-specific and exists in v0.20: a failing plugin
  delete call silently orphans resources today. It is filed separately as #3058 and this design
  does not fix it for the non-strict path.

D4 widens the stakes of the warn-only default. A non-secret setting such as a table, bucket or topic
that resolves to blank can misroute data silently; the per-start warning and `pipelines status`
entry are there for that reason, and strict mode is recommended for production in the runbook.

This is open decision D2. It is the one place where the secure default and the compatibility
policy pull apart.

## Interactions with existing behavior

### `"***"` redaction and #2926

- `toproto` redaction emits a key for every key in `Settings` **and** `SettingRefs`, each `"***"`.
  Without this, a provisioned connector's reference keys would vanish from `GET` responses, and the
  response shape (a flat `map<string,string>`) is the contract. No proto change.
- The API does not reveal that a key is a reference or what the template is. Templates carry
  variable names and hostnames; `"***"` stays the only output.
- `restoreRedactedSettings` operates on `Settings` and is unchanged. It never sees a reference:
  `SettingRefs` is only ever non-empty on entities created by the file parser, which are
  `ProvisionTypeConfig`. `orchestrator.ConnectorOrchestrator.Update` rejects any non-API entity
  with `CodeImmutableProvisionedByConfig` before it calls `restoreRedactedSettingsForPlugin`
  (`pkg/orchestrator/connectors.go`), and the processor path does the same
  (`pkg/orchestrator/processors.go`). So the keep-stored-value
  logic never sees a reference-bearing entity. A test pins that ordering, so a future change that
  allows API edits of provisioned entities has to confront references explicitly.
- `RefuseRedactedSettings` is unaffected: documents from the API have no `SettingRefs`.

### API and wire

- No new proto field. `ApplyPipeline`/`PlanPipeline` documents carry literals only, and
  `enrichAndValidate` keeps refusing `"***"`. References never travel over the API (fact 2).
- `pipelines deploy|apply` keep expanding client-side (default parser mode). Their settings are
  stored as literals in v0.21. That leaves a hole for operators who deploy by CLI instead of by
  directory provisioning. Closing it needs either a proto field for references plus an operator
  opt-in so a remote caller cannot read the engine's environment, or a local-only mode of the CLI;
  both are out of scope here (D3, follow-up).

### `pipelines export`

None exists. If one is built, it must emit `SettingRefs` as templates under `settings:` (so the
output round-trips to the file) and must never resolve. The doc for it must say so.

### Provisioning and diff

`Export` returns `Settings` plus `SettingRefs`. The `actionsBuilder` compares both. A file that
changes `${A}` to `${B}` is a connector update (restart-class, same classification as any connector
settings change). A file that keeps `${A}` while the value of `A` changes is a no-op (see section 4).

### Hot reload (`conduit run --dev`)

`pkg/conduit/dev` parses in the engine process, so it uses `KeepSettingRefs` and behaves like
startup provisioning. Editing the file to change a reference triggers `ApplyPlanLive` as today.
Changing only the environment of a running engine is not observable by a watcher and is not a
supported reload path.

### Plan, validate and lint

`conduit pipelines validate|lint` and `generate` keep expanding (default mode), so schema-level
checks that need a value (types such as `${PORT}` against an int parameter) see one. Unset variables
there are a `warn` finding, not a failure, matching the rollout in section 5.

## Alternatives

### A. Store the reference, resolve at start (chosen)

Covered above. Fit with v0.22: the template string and the single `Resolve` function are the seam.
v0.22 extends the grammar (`${vault:path#key}`, `${k8s:ns/name:key}`, in the style of Kafka Connect
config providers, which the Kafka Connect migration already maps) and adds providers behind
`Resolve`. Stored data written by v0.21 stays valid, no migration. Today `${vault:x}` expands to
`""` silently (env var named `vault:x`); that is why the unset-variable rollout in section 5 matters
before the grammar can grow.

Pros: no secret in the DB for the targeted case; no key to lose, rotate or back up; small (one
field, one function, a handful of call sites); downgrade self-heals when the v0.20 reconcile succeeds (see _Downgrade_);
nothing new to operate.

Cons: does not protect literals (API, CLI deploy); changes the "env change is a config change"
behavior; the secret still lives in the environment, which a plugin inherits.

### B. Envelope-encrypt secret fields with a key from env or KMS

A store-level encode/decode hook encrypts each settings value (AES-256-GCM, AAD = entity ID +
settings key + format version, key ID in the envelope for rotation) as it is written, and decrypts
on read. Covers literals from the API too.

Why it lost for v0.21:

- **It needs the field set it does not have.** No sensitivity metadata, so it must encrypt every
  settings value. That is workable, but then an operator can no longer read config out of the DB
  with `psql`, and every non-secret becomes opaque.
- **Key management is the whole problem.** The key comes from the same environment the secrets
  came from, so it protects against DB theft and nothing else. A KMS key is the real answer, and
  that is the v0.22 provider work, not a v0.21 side quest.
- **Key loss is data loss for API-created entities.** File-provisioned entities re-derive from the
  file; API-created ones cannot. Rotation needs an online re-encrypt pass over every entity, a new
  crash-recovery surface (Invariant 5 applies to a migration that rewrites the whole entity set).
- **Downgrade is bad.** A v0.20 binary reads ciphertext as the setting value and passes it to the
  connector: authentication failures against production systems, repeated, and possibly account
  lockouts. Reference storage downgrades to "setting missing" for the same entities, which fails
  closed and then self-heals.
- **Format risk.** It changes the encoding of every settings value, not just references.

It is complementary, not exclusive. If demand for protecting literals is real, B is the follow-up,
built on the v0.22 provider/KMS work so the key is not an env var.

### C. A pluggable secret-provider interface now (Vault, KMS, Kubernetes), as the roadmap lists for v0.22

Why not now: CLAUDE.md requires two real implementations or a design doc justifying the seam. We
would ship one (environment). The interface would also be designed without the Vault and KMS
requirements known: auth, leases and renewal, caching, failure semantics when the provider is down
at start, and whether a provider can return a different value per call. Building it early would
lock in guesses.

What this design does to keep the door open without building it: the stored value is a template
string and resolution is one function. A provider is a new case inside `Resolve` plus a grammar
extension, and `secretref.env_unset` generalizes to a provider error code. No stored data needs to
change.

### D. Reuse the `CONDUIT_SECRET_*` resolver (#2988 option 2)

`pkg/plugin/processor/egress/secret.go` maps a granted name to `CONDUIT_SECRET_<NAME>` and returns a
whole `Authorization` header for the WASM host. Its namespace is a security boundary against an
untrusted guest: only operator-granted names, only the exported `CONDUIT_SECRET_*` set. In a config
file the operator is the trusted party and already references arbitrary variables, so imposing the
prefix breaks every existing `${VAR}` for no benefit. The resolvers have different trust models and
should stay separate. A shared namespace or allowlist for config-file references can be revisited
with the providers in v0.22.

### E. Do not persist settings of file-provisioned entities at all

They are re-derived from the file on every start, so the DB copy looks redundant. It is not: the
API lists and describes them, a pipeline restart inside one process uses the stored copy, the diff
needs the stored side, and the file can change or be briefly unreadable under a running engine.
Rejected as a larger change with weaker failure behavior than A.

## Failure modes

| Situation | Behavior | Invariant / note |
| --- | --- | --- |
| Variable unset at pipeline start | v0.21: warn with `secretref.env_unset`, resolve to `""` (as today). Strict: pipeline degraded, coded cause naming variable, entity and path; plugin not configured. | Fail-closed in strict. No partial config reaches a plugin. |
| Variable set but wrong or rotated | Same as any bad credential today: the plugin's own error. Rotation = update environment, restart engine. | A stop/start inside the process does not re-read the environment (documented). |
| Variable empty string | Used as is. | Explicit empty is deliberate. |
| Env differs between runs | Resolved fresh each start. Different value, same stored template: no update, no lifecycle event. | Behavior change, release notes (section 4). |
| Config file deleted or edited while engine runs | Unchanged from today: reconcile at next start, or via the dev watcher. | |
| Crash (SIGKILL) mid-migration of one pipeline | The per-pipeline transaction (`provisionPipeline`) rolls back; the old plaintext record is intact and the next start migrates it. | Invariant 5 via the store's transactional API; Invariant 7. |
| Crash between two pipelines' migrations | Pipelines are independent: some migrated, some not. Each start continues. Idempotent. | A startup summary log says how many remain. |
| Migration of one pipeline fails (parse error, validation error) | That pipeline keeps its plaintext record (and is skipped as today); other pipelines proceed. Coded warning `provisioning.settings_refs_migration_incomplete`. | Never block startup for this. |
| Position state during migration | `Instance.State` is carried by the update path (`connector.Service.Update` only replaces `Config`/`Plugin`). Test asserts byte-equal `State` before and after. | Invariant 2. |
| A code path assigns resolved settings into `Instance.Config` | Would be persisted by the persister within seconds. Prevented by the type in R1 and caught by the canary scan on every store backend. | The riskiest implementation bug; the scan is a merge gate for the implementation PR. |
| Key loss | Not applicable: no Conduit-held key. Loss of the environment value is the unset case above. | A property of A. |
| Provider outage (v0.22) | Out of scope; the resolve-failure path above is the place to define it then. | |
| Downgrade to v0.20.x | Self-heal only when the v0.20 reconcile succeeds; otherwise reference-backed keys are absent at start. See _Downgrade_. | Not fail-closed without a v0.20.x patch (D9). |
| Unset variable in `sdk.egress.*` | Hard failure at processor open, regardless of strict mode. | Fail closed. |
| Delete event cannot resolve (strict) | Preflight before the store delete fails the delete action with `secretref.env_unset`; the record survives and `deleteOldPipelines` retries. All connectors of the pipeline are preflighted first. | See section 5. The wider ordering problem is #3058. |
| `ApplyPipeline` onto a file-provisioned pipeline | Overwrites references with literals until the next start's reconcile. | Documented (section 3). |
| Old data left in storage after migration | See _Residue_. | |

## Upgrade and rollback

### Migration

There is no separate migration job. On the first v0.21 start:

1. `provisioning.Service.Init` parses each file with `KeepSettingRefs`.
2. `Export` returns the stored config: literals in `Settings`, no `SettingRefs`.
3. The diff sees the setting move from `Settings` to `SettingRefs` and emits a connector (or
   processor, or pipeline-DLQ) update.
4. `provisionPipeline` applies it inside its existing `db.NewTransaction`, and commits.

That is the reconcile that already runs on every start, so the migration path is the tested one.
Two details need explicit code, covered by tests:

- **`LastActiveConfig`.** `connector.Service.Update` replaces `Config` and leaves
  `LastActiveConfig` alone, so the old plaintext would stay in the stored record until the connector
  next starts. The update path must rewrite `LastActiveConfig`: every key of it that is now a
  reference in `Config` becomes the same reference. A `LastActiveConfig` key that is not a reference
  in `Config` is untouched. Consequence: for migrated keys the lifecycle "was it changed" signal
  compares templates, so a change made only in the environment between runs is not an update (the
  same behavior change as section 4).
- **Entities that were not in a file.** Anything created through the API, `ApplyPipeline` or CLI
  deploy has literals and keeps them. They are not touched, not counted as failures and not
  readable as references.

A start with no config files changes nothing. A start where the environment lacks a variable (the
unset case) migrates the template and warns; it does not need the value to migrate.

### Upgrade test (required by the lane DoD)

On N (v0.20.x) create a database per backend (badger, SQLite, Postgres) with pipelines whose
settings use `${VAR}` and with state; upgrade to N+1; verify: pipelines resume from their
positions, the raw database contains no canary value, `SettingRefs` is populated, no lifecycle
event fired. See _Tests_.

### Downgrade

A v0.20.x binary decodes the v0.21 record, ignores the unknown `SettingRefs` field and sees a
connector whose reference-backed settings are **absent**.

**Self-heal holds only when the v0.20 reconcile succeeds.** On its next start the old binary parses
the same file, expands the variable, diffs against the stored config (setting missing), and writes
the literal back. That downgrade then re-introduces plaintext at rest for those entities, stated
in the release notes and the runbook. But the path is not fail-closed (verified against v0.20.0):

- `provisioning.Service.Init` continues past a pipeline that fails to provision
  (`pkg/provisioning/service.go`, the error is collected and the loop moves on).
- `ExitOnDegraded` defaults to false (`pkg/conduit/runtime.go`), so a failed provisioning does not
  stop the engine.
- `lifecycleService.Init` auto-resumes pipelines that were running (`pkg/lifecycle/service.go`).

So if the v0.20 reconcile fails for a pipeline, or `pipelines.path` is not mounted on the downgraded
host, the pipeline starts with its reference-backed keys **absent**. For a required key the plugin
rejects the config. For an optional credential it falls back silently: the S3 connector uses the
default AWS credential chain, a Kafka connector runs without SASL. That is a change in which
identity the pipeline uses, not a clean failure.

Two options, D9:

- **(a) A v0.20.x patch.** v0.20.2 refuses to start a connector, processor or DLQ whose stored
  record has a non-empty `SettingRefs` it does not understand: the pipeline fails loudly as
  degraded with a coded cause that names the downgrade, instead of running with missing keys. The
  patch decodes the field as an opaque map and checks it is empty. It does not need to resolve it.
  Only helps for downgrades to a release that carries the patch; v0.20.0 and v0.20.1 remain
  exposed, which is stated in the release notes.
- **(b) Document it.** State that the self-heal holds only when the reconcile succeeds and the
  pipelines directory is mounted, and tell operators to downgrade only after confirming both.

Recommendation: **(a), in v0.20.2** (being cut now), plus the (b) documentation for older v0.20
releases. Pending DeVaris. Without (a), the downgrade guidance is "do not downgrade across this
change unless you have verified the reconcile".

Pipelines that were running before the downgrade are auto-resumed after provisioning
(`provisioning.Service.Init` precedes `lifecycleService.Init`), so on the success path they start
with a complete config. Entities whose file is gone are deleted by the old binary's own
`deleteOldPipelines`, as today.

There is no format marker in the database, so a binary that predates the field cannot refuse on its
own; (a) is the only way to give one that behavior.

**Required downgrade tests.** N+1 provision, N start:

1. Success path: the pipeline runs and the settings are literals again.
2. Failed reconcile: make the v0.20 provisioning of that pipeline fail (invalid file for that
   pipeline). Assert the outcome; with (a), the pipeline is degraded with the coded cause and does
   not run; without it, record that it ran with the keys absent.
3. Missing path: start v0.20 with `pipelines.path` unset or empty. Same assertions.
4. An optional-credential connector (S3-style, or a stub with a default-chain fallback) in cases 2
   and 3, to prove which identity it would have used.

### N+1 readability

v0.22 must read v0.21 records. The field is additive and the template grammar only grows. Records
written by a v0.22 that uses provider syntax are not readable as meaningful config by v0.21 (it
would treat `${vault:x}` as an env lookup of `vault:x`); that is the normal forward-compat boundary
and belongs in the v0.22 design.

### Residue: data already written

Rewriting a key does not erase the old bytes.

- **badger:** old values stay in the value log and SSTables until compaction and GC.
- **SQLite:** old pages are on the freelist or in the WAL until `VACUUM`/checkpoint; `secure_delete`
  is off by default.
- **Postgres:** dead tuples until vacuum, plus WAL, archives, replicas and `pg_dump` files.
- **Backups and snapshots taken before the upgrade** contain the secret, unconditionally.

Conduit does not ship scrubbing tooling in this change. The runbook says: treat every secret that
was ever passed as `${VAR}` before v0.21 as exposed to whoever could read the database or its
backups, and rotate it. Compaction commands per backend are given as hygiene, not as a guarantee.
Whether Conduit should offer a scrub command is D6 (recommendation: no).

## Observability

- **Error codes** (registered with `conduiterr.Register`, stable, each with a config path and a
  suggestion):
  - `secretref.env_unset` (`FailedPrecondition`): variable unset at resolution.
  - `provisioning.settings_refs_migration_incomplete` (a warning, not returned as an error):
    a pipeline could not be migrated.
- **Logs:** one startup line from provisioning: entities with references, entities migrated this
  start, entities still holding literals that a file reference could replace. Variable **names**
  only, never values, matching `plugin.standalone_env_passed`. A warning per unset variable (once
  per variable per start).
- **Per-start warnings and status:** the `secretref.env_unset` warning is per pipeline start and is
  mirrored in `pipelines status` and the degraded cause (section 5).
- **Metrics:** none new in v0.21. Counters are cheap, but nothing in an at-rest property is worth
  paging on; the degraded-pipeline cause carries the failure.
- **`conduit doctor`:** a `secrets.env` check (with `--json`, like `plugins.standalone_env`) that
  parses the configured pipelines directory with `KeepSettingRefs` and reports variable names that
  are unset in the doctor's environment. The MCP `doctor` tool reports counts only, as the plugin
  environment check does, because its environment may differ from the engine's.
- **Linter:** a warning when a reference appears outside a settings value (`id`, `plugin`, ...),
  since those are stored expanded.
- **Docs and runbook:** `docs/operations/secrets-at-rest.md` (symptom, diagnosis, remediation):
  unset variable at start; rotating a secret; what the downgrade does; scrubbing old data and
  backups; what is and is not protected. Release-notes migration entry. Remove the known-issue text
  for #2988. `conduit.io` docs source and `llms.txt` updated together, with the config reference
  stating that references in settings are stored as references and that API-supplied values are
  stored as given.

## Tests (for the implementation PRs)

Tier 1 bar. The implementation is split so each PR is reviewable: (1) `pkg/secretref`, the parser
option, the `SettingRefs` field, `PrepareSet` and store round-trip tests; (2) the call sites; (3) the reconcile
migration including `LastActiveConfig`; (4) doctor, linter, docs and runbook.

- **Store round trip (blocker-class).** `connector.Store.PrepareSet` -> `Get` round-trips
  `Config.SettingRefs` and `LastActiveConfig.SettingRefs`, and so do the processor and pipeline
  stores. A second test captures what the **persister** snapshot writes (`Persister` ->
  `PrepareSet`) and asserts both fields survive. Golden tests cannot catch a drop because of
  `omitempty`.
- **Canary scan (merge gate).** Use an **alphanumeric** canary (`json` escapes `<`, `>` and `&`, so
  a canary with them would pass the scan while leaking). Provision from a file referencing it, run
  the pipeline with records flowing until several position flushes and a restart have happened,
  then scan the raw bytes. Zero hits, with a **positive control** per backend: plant the canary as
  a literal setting through the API and assert the scan finds it, so a scan that cannot see the
  bytes fails loudly.
  - badger: Snappy-compressed by default, so scan the decoded key/value contents, and also scan
    the `.mem`, WAL and `vlog` files; the control proves which of these actually contain it.
  - SQLite: the database file and the `-wal` file.
  - Postgres: a row scan of the table plus a `pg_dump`; WAL and dead tuples are residue and not
    part of the gate (see _Residue_).
  - In-memory: a walk of the store.
  - Matrix: connector, processor, **DLQ** and `sdk.egress.*` keys each set with a reference;
    after a failed start; after an update; after a live `ApplyPlanLive` update; after
    `UpdateWhileRunning` (`processor.Service`).
- **Trust model.** No `fromproto`/API path can populate `SettingRefs`; `Plan`, `ApplyPlan` and
  `ApplyPlanLive` cannot be handed references by a non-parser caller (a compile-level property of the
  `parsed.Refs` type, plus a test that `fromproto` output has the zero value); `ApplyPipeline` onto a
  file-provisioned pipeline behaves as section 3 states.
- **All-references connector** across migrate, restart and delete: `OnDelete` fires; no spurious
  update event; `LastActiveConfig` "never started" is judged on both maps.
- **Strict delete preflight.** An unset variable in a connector's `LastActiveConfig` makes the delete
  action fail with the coded error before `store.Delete`; the record and its `State` survive;
  a multi-connector pipeline deletes none of its connectors; after the variable is set the retry in
  `deleteOldPipelines` succeeds.
- **No in-place mutation** of shared `Settings`/`SettingRefs` maps (race detector).
- **Grammar equivalence (fuzz).** `Resolve(template)` equals `os.ExpandEnv(template)` when every
  variable is set, for all inputs; "is a reference" is exactly "`os.Expand` called the mapper". Seed
  corpus runs in the normal `test` job, per CLAUDE.md fuzzing standard; add a `Fuzz*` target.
- **Property:** `decode(encode(Config))` round-trips with `SettingRefs`; a key is never in both
  maps; an entity with no refs encodes byte-identically to v0.20.
- **Upgrade, downgrade, resume** as in _Upgrade and rollback_, per backend.
- **Kill-mid-migration (chaos, `tests/chaos`):** SIGKILL between the update and the commit and
  between pipelines; the next start finishes; `State` is byte-equal; no record is lost or
  duplicated beyond at-least-once.
- **Redaction:** `GET` of a provisioned connector returns every key (settings and references) as
  `"***"`; ordering test pinning the immutability check before `restoreRedactedSettings`.
- **Unset variable:** warn path and strict path; error message names variable, entity and path and
  contains no value.
- **Persister:** a test that a resolved value assigned to `Instance.Config` is detected (the canary
  scan is the guard; this one proves the guard fires).
- Bug-fix rule: every defect found in review gets its test first.

## Non-goals

- Encrypting anything at rest (alternative B). Reasons above.
- A provider interface, Vault, KMS or Kubernetes secrets (v0.22).
- References over the API or from `pipelines deploy|apply`.
- Restricting which variables a reference may read.
- Plugin environment restriction (separate v0.21 item).
- Scrubbing physical storage.
- Marking connector parameters as sensitive (#2566).

## Open decisions

Each with a recommendation. DeVaris decides.

Approved by DeVaris: D1, D3, D4, D6, D7, D8, as recommended. D2 and D5 are refined below after
review. D9 is new.

- **D1 (approved). Scope: reference storage only, or also encryption (B).** Recommend reference storage only.
  It closes #2988 as filed, adds no key to lose, and sets up v0.22. State the literal-secret
  residual in the release notes.
- **D2 (refined). Unset variable.** Recommend warn in v0.21, strict by default in v0.23, with:
  the warning per pipeline start and visible in the degraded cause, `pipelines status` and doctor;
  a hard failure for unset variables in `sdk.egress.*` keys; and a defined strict-mode behavior for
  delete events (preflight before the store delete in strict mode, no force flag, every connector of
  the pipeline checked first). The case for erroring now is real
  (an empty credential should never reach a plugin, and D4 means non-secret keys such as table or
  topic can resolve to blank and misroute); the cost is breaking configs that rely on
  unset-is-empty.
- **D3 (approved). `pipelines deploy|apply` and `ApplyPipeline`.** Recommend leaving client-side expansion
  (literals stored) in v0.21 and filing a follow-up. A proto field for references needs an operator
  opt-in so an unauthenticated caller cannot read the engine's environment.
- **D4 (approved). All settings references, or only "secret-looking" ones.** Recommend all settings
  references (no sensitivity metadata exists).
- **D5 (refined). `LastActiveConfig` rewrite and lifecycle semantics.** Recommend: migrated keys
  are rewritten to the same reference; "never started" and "equal" are defined over Settings plus
  SettingRefs; compare uses templates and the event payload is resolved, so `ConfigBefore` in an
  update event carries current-environment values. Accepted consequence: an env-only change no longer
  fires a lifecycle update.
- **D6 (approved). Scrubbing tooling.** Recommend no tooling; document rotate-and-compact, and be explicit
  that pre-upgrade backups cannot be fixed.
- **D7 (approved). `secretref` as a package name and its placement** (`pkg/secretref` versus under
  `pkg/foundation`). Recommend `pkg/secretref`; low stakes.
- **D8 (approved). Exposing "this key is a reference" in the API.** Recommend no (keeps `"***"` the only
  output). Revisit with the sensitivity marker.

- **D9 (new, pending DeVaris). Downgrade safety.** Recommend a v0.20.2 patch that refuses to run a
  record with a `SettingRefs` it does not understand (degraded with a coded cause), plus
  documentation for v0.20.0 and v0.20.1. Without it the downgrade is not fail-closed: a failed
  reconcile or an unmounted pipelines path starts pipelines with reference-backed keys absent, and
  optional credentials fall back silently (S3 default chain, Kafka without SASL).

## Related

- #2988 (the issue), #3018 (lane), #3002 (v0.21 tracking), #2926 and #2913 (`"***"` redaction and
  round trip).
- `conduit-connector-s3`#1021 (credential chain, reduces the need for static keys).
- #2566 (sensitive parameter marker, referenced from `pkg/foundation/log/redact.go`).
- #3058 (connector delete runs `OnDelete` after the record is gone; pre-existing, not secrets-specific).
- v0.22 roadmap: external secret stores (Vault, KMS, Kubernetes).
- `docs/design-documents/20260726-wasm-host-egress-capability.md` (the `CONDUIT_SECRET_*` resolver).
- `docs/operations/standalone-plugin-environment.md` (plugins inherit the host environment).
- `docs/design-documents/20260706-degraded-pipeline-error-cause.md`.
