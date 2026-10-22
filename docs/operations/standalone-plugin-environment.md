# Standalone plugin environment: `connectors.standalone.env.allow`

Standalone connector plugins run as separate processes, as the same OS user as Conduit. They no
longer inherit Conduit's environment. A plugin receives only:

| Variable | Why |
| --- | --- |
| `PATH`, `HOME` | plugins that run helper programs or resolve `~` |
| `TMPDIR`, `TEMP`, `TMP` | the temp directory (go-plugin puts its unix socket there) |
| `TZ`, `LANG`, `LANGUAGE`, `LC_*` | time zone and locale |
| `SYSTEMROOT`, `WINDIR` (Windows only) | the Go runtime and Winsock do not start without them |
| names in `connectors.standalone.env.allow` | whatever the operator adds |
| `CONDUIT_CONNECTOR_*` | set by Conduit for each plugin (id, token, log level, utilities address, max record size) |
| `PLUGIN_*` and the go-plugin magic cookie | the handshake, set by go-plugin |

`CONDUIT_CONNECTOR_*` and `PLUGIN_*` are never taken from the host, even if listed. Names are matched
exactly (case-insensitively on Windows). The base set was checked against the code that reads the
environment on the plugin side (the connector SDK reads only the `CONDUIT_CONNECTOR_*` variables;
go-plugin reads the handshake variables and `PLUGIN_UNIX_SOCKET_DIR`/`_GROUP`, which are not passed
and fall back to the temp directory), and a test starts a real plugin with exactly this environment.

## Setting

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.standalone.env.allow` | `CONDUIT_CONNECTORS_STANDALONE_ENV_ALLOW` | empty | Extra host variables plugins receive (repeatable, or comma/space separated). Exact names; wildcards are a startup error |

```yaml
connectors:
  standalone:
    env:
      allow: [HTTPS_PROXY, SSL_CERT_FILE, AWS_REGION]
```

## Symptom after upgrading

A plugin that read a host variable now sees it unset: a missing credential, a proxy that is
ignored, a certificate bundle that is not found. At startup Conduit logs one warning (once per
process) that lists the variable **names** it is not passing, never values:

```text
WRN standalone plugin processes no longer inherit the engine's environment: the variables listed are not
    passed to them ... code=plugin.standalone_env_stripped env_names_stripped=["AWS_ACCESS_KEY_ID","HTTPS_PROXY",...]
```

Names that are not plain identifiers are counted (`env_non_standard_names_stripped`), not printed.

## Migrating

1. Start Conduit once on the new version and read the `plugin.standalone_env_stripped` warning.
2. Add the names your plugins need to `connectors.standalone.env.allow`. Prefer giving a plugin its
   credentials through its own connector settings (which can use `${VAR}` for names in `env.allow`,
   where that control exists) over exporting them to every plugin.
3. If you list a name that looks like a credential, the warning `plugin.standalone_env_passed` names
   it so the choice is visible in the log. `conduit doctor --check plugins.standalone_env` shows the
   same list (names only; the MCP tool shows a count).
4. When you have reviewed the lists, silence both warnings with
   `connectors.standalone.env-warning: false` (or `--connectors.standalone.env-warning=false`,
   `CONDUIT_CONNECTORS_STANDALONE_ENV_WARNING=false`; embedders: `Options.SilenceStandalonePluginEnvWarning`).
   This changes nothing about what plugins receive.

Embedders set `Options.StandalonePluginEnvAllow`.

`conduit doctor`'s plugin spec check runs plugins with the base set only (it does not know your
allow list); a plugin that cannot start without an allowlisted variable may fail that check and work
under `conduit run`.

## What is not covered

- This limits what a plugin inherits from Conduit's environment. A standalone plugin is still native
  code with the privileges of the Conduit user: it can read any file that user can read, including
  Conduit's configuration and database, and it can use the network. Install only plugins you trust.
- The path controls in [file-access](file-access.md) (`connectors.file.allowed-dirs` and friends)
  apply to **built-in** connectors only. A standalone connector opens whatever its own code opens.
  The exception is a standalone connector named `file`, which gets the same path check.

## Alerting

Alert on the log fields `code=plugin.standalone_env_stripped` (variables withheld) and
`code=plugin.standalone_env_passed` (sensitive-looking variables you allowlisted), or on the doctor
finding code `plugin.standalone_env_passed`.
