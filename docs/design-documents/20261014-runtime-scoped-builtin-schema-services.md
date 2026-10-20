# Runtime-scoped schema services for built-in plugins

## Summary

Built-in connectors and processors reach the schema registry through package-level variables in the
connector and processor SDKs (`schema.Service`, `schema.SchemaService`). Every `NewRuntime` used to
overwrite them with its own service, so in a process with two runtimes the first runtime's built-in
processors silently resolved schemas in the second runtime's registry, and its built-in connectors
failed with `invalid token` ([#3088](https://github.com/ConduitIO/conduit/issues/3088)).

Conduit now installs a stateless router into each SDK variable, once per process. Every call Conduit
makes into a built-in plugin carries the owning runtime's schema service in its context, and the
router forwards each schema call to the service in the context. A call with no service in its context
is refused with `plugin.builtin_schema_unscoped`. No SDK change, no protocol change, no serialized
format change.

## Context

- `pkg/plugin/processor/builtin/registry.go` set processor-sdk `schema.SchemaService` to a service over
  the runtime's registry. The processor schema service has no caller check, so the wrong-registry
  lookup was silent: records decoded and re-encoded against another runtime's schema, then acked
  (invariant 6).
- `pkg/plugin/connector/builtin/registry.go` set connector-sdk `schema.Service`. The connector schema
  service checks the connector token, so the first runtime's connectors failed loudly.
- The `conduit` CLI runs one runtime per process and was not affected. The embed API (`conduit.New`)
  allows several.
- Built-in plugins already receive Conduit's context on every call. The connector token reaches the
  schema service that way (the SDK's `internal.Enrich` adds it, `ccontext.Detach` keeps values), which
  is evidence that context values reach the SDK schema calls on all connector paths that work today.

## Constraints

- No change to `conduit-connector-protocol` or the SDKs' public API in this release.
- A wrong-registry answer must be impossible, not unlikely. Refusing is acceptable; guessing is not.
- No write to the SDK variables after the first runtime exists: running plugins read them, so a later
  write is a data race.

## Decision

1. `pkg/plugin/{connector,processor}/builtin/schema_scope.go` define an unexported context key and a
   stateless `scopedSchemaService` that implements the SDK's schema service interface. It looks up the
   service in the context and forwards, or returns `plugin.CodeBuiltinSchemaUnscoped`.
2. `NewRegistry` installs the router with `sync.Once`.
3. Processors: `processorWithID`, the outermost wrapper of every built-in processor (outside the SDK
   schema middleware), adds the runtime's `procutils.SchemaService` to the context of `Configure`,
   `Open`, `Process` and `Teardown`.
4. Connectors: the builtin dispenser hands the runtime's `connutils.SchemaService` to the source and
   destination adapters, whose `pluginContext` adds it to every call, including `Run`. The connector
   schema service still checks the token.

`pprocutils.Logger` stays process-wide (log routing only, not data); documented in `doc.go` and tracked
in [#3111](https://github.com/ConduitIO/conduit/issues/3111).

## Alternatives considered

- **Refuse a second runtime in the process** with a coded error. Simple and safe, but it breaks the
  embed API's documented two-Engine use and existing tests (`two_engines_test.go`,
  `TestRuntime`), and it leaves the globals in place. Kept as the fallback had the SDKs forced a
  global; they don't, because the global is an interface Conduit controls.
- **SDK seam** (per-plugin schema service passed through the plugin constructor or `PluginConfig`).
  The cleanest end state, but it needs coordinated SDK releases and, for connectors, possibly a
  protocol change. The context router gives the same isolation now and does not block that seam
  later.
- **Router keyed by connector token / processor ID** in a process-wide map. Needs registration and
  cleanup on every plugin lifecycle and a lock on the hot path; the context already carries the
  answer.
- **Fall back to the most recent runtime when the context has no scope.** Reintroduces the bug for
  exactly the calls we cannot attribute. Rejected.

## Failure modes

- **A built-in plugin resolves schemas from a context Conduit did not hand it** (for example a
  goroutine on `context.Background()`). Processors: before, the call was answered by the last
  runtime's registry (correct only with one runtime); now it fails with
  `plugin.builtin_schema_unscoped`, and the record follows the normal error path (DLQ or degraded
  pipeline). No in-tree built-in processor does this: the only schema callers are the SDK middleware
  and the avro processors, which use their own injected registry. Connectors: such a call already
  failed with `invalid token` before this change, so there is no new failure.
- **Host or embedder code calls the SDK schema packages outside any plugin call** (for example an
  application seeding or reading schemas through `conduit-processor-sdk/schema.Create` or
  `conduit-connector-sdk/schema.Get` on its own context). Before, this was answered by the most
  recently created runtime's registry; now it is refused with `plugin.builtin_schema_unscoped`. With
  more than one runtime there is no correct answer to give, and with one runtime the registry is
  reachable directly. In-tree, only tests did this (`schema_fidelity_test.go` now scopes its context).
  Called out in the v0.21 release notes.
- **Embedder-supplied built-ins** (`Config.ProcessorPlugins`, `Config.ConnectorPlugins`). They are
  dispensed through the same registries and wrappers, so they are scoped like Conduit's own. One that
  resolves schemas on a context it did not receive from Conduit is refused, as above, where before it
  silently used the last runtime's registry. The SDK middleware always uses the call context, so a
  plugin built on the SDK middleware is unaffected.
- **A wrapper between Conduit and the plugin drops context values.** Not possible with
  `context.WithValue` descendants; `ccontext.Detach` preserves values.
- **Two runtimes created concurrently.** `sync.Once` makes the schema service install race-free, and the
  router has no per-runtime state. This holds for the schema services only: `NewRuntime` still writes
  `zerolog.DefaultContextLogger` (`pkg/conduit/runtime.go`, CLI logger path) and `NewRegistry` still
  writes `pprocutils.Logger` on every call, so concurrent creation races on those two loggers. That is
  pre-existing and log-only; tracked in [#3111](https://github.com/ConduitIO/conduit/issues/3111).
- **Runtime closed while another lives.** Nothing to clean up: the service lives only in contexts of
  that runtime's plugins.
- **Crash, partition, duplicates, ordering:** unchanged; this changes which registry answers, not
  when records are acked or in what order.

## Upgrade and rollback

No persisted state, config key or protocol message changes. Rolling back restores the previous
behaviour (single runtime correct, multi-runtime cross-talk). Chaos and upgrade suites are unaffected:
neither runs more than one runtime per process, and the single-runtime path resolves against the same
registry as before.

## Observability

The refusal carries a stable code (`plugin.builtin_schema_unscoped`, gRPC `Internal`) and a
suggestion, and shows up wherever record errors surface (DLQ metadata, pipeline error, logs).

## Testing

`pkg/conduit/runtime_schema_scope_test.go` builds two runtimes with distinct registries holding a
different schema under the same subject and version, and probe built-in plugins that call the SDK
schema packages as the SDK middleware does. Each runtime's processor and connector must see only its
own schema, the schema a connector creates must land only in its own registry, a destination behind
the SDK's real batching and schema decode middleware (`sdk.batch.delay` set, so the flush and decode
run on the SDK's timer goroutine) must decode with its own runtime's schema, and an unscoped call must
be refused with the code. It fails on main (wrong schema for runtime A's processor, `invalid
token` for A's connector).

## Related

- [#3088](https://github.com/ConduitIO/conduit/issues/3088), tracking issue #3002, #3012.
- `doc.go` (embed API known limitations).
