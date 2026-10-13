# Standalone plugin environment: `plugin.standalone_env_passed`

Standalone connector plugins run as separate processes. When Conduit starts one, the plugin
process receives Conduit's own environment variables in addition to the ones Conduit sets for it
(`CONDUIT_CONNECTOR_*`). This note covers the startup warning that lists those variables and what
to do about it.

## Symptom

At startup, when `connectors.path` contains at least one standalone connector, Conduit logs one
warning:

```text
WRN host environment variables are passed to standalone plugin processes; v0.21 will restrict the
    plugin environment to an allowlist (...) code=plugin.standalone_env_passed
    env_names=["AWS_ACCESS_KEY_ID","AWS_SECRET_ACCESS_KEY","DATABASE_URL"] docs=docs/operations/standalone-plugin-environment.md
```

`conduit doctor` reports the same condition as a `warn` finding named `plugins.standalone_env`
with the code `plugin.standalone_env_passed`:

```bash
conduit doctor --check plugins.standalone_env --json
```

The warning is logged once per process, not once per plugin. It lists variable **names** only.
Values are never logged.

## What is listed

Names of host environment variables that standalone plugins will receive and that match one of:
`AWS_*`, `AZURE_*`, `GOOGLE_*`, `GCP_*`, `CONDUIT_*` (excluding `CONDUIT_CONNECTOR_*`, which the
engine sets itself), `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `*_KEY`, `*_CREDENTIALS`, and names
ending in `DATABASE_URL`, `DATABASE_URI`, `DB_URL` or `DB_URI`. Matching is case-sensitive, except
on Windows where it is case-insensitive.

The list is advisory. Other variables are passed too; the patterns only pick out names that look
like credentials.

## What to do

1. Install only plugins you trust. A standalone plugin is native code running as the same OS user
   as Conduit, with the environment above.
2. Launch Conduit with a minimal environment. Provide only the variables Conduit and your
   connectors need, for example through the service manager's environment settings or
   `env -i PATH=... conduit run`. Prefer a secrets mechanism your connectors read directly over
   exporting credentials into the Conduit process.
3. Once you have reviewed the list and accepted it, silence the warning:

   ```yaml
   connectors:
     standalone:
       env-warning: false
   ```

   or `--connectors.standalone.env-warning=false`, or the environment variable
   `CONDUIT_CONNECTORS_STANDALONE_ENV_WARNING=false`.
   This only silences the warning and the doctor finding. It does not change what plugins receive.

## What changes in v0.21

v0.20.2 only warns; the environment plugins receive is unchanged. v0.21 will restrict the plugin
environment to an allowlist (tracked in #3043). Plugins that read variables from the host
environment will then need those variables listed explicitly. Use the names in this warning as the
starting point for that list.

## Alerting

Alert on the log field `code=plugin.standalone_env_passed`, or on the doctor finding code of the
same name.
