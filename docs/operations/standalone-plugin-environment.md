# Standalone plugin environment: `plugin.standalone_env_passed`

Standalone connector plugins run as separate processes. When Conduit starts one, the plugin
process receives Conduit's own environment variables in addition to the ones Conduit sets for it
(`CONDUIT_CONNECTOR_*`). The host environment is appended after Conduit's values, so when a host
variable has the same name as one Conduit sets (for example `CONDUIT_CONNECTOR_TOKEN`), the host
value is the one the plugin sees. This note covers the startup warning that lists the host
variables and what to do about it.

## Symptom

At startup, when `connectors.path` contains at least one standalone connector, Conduit logs one
warning:

```text
WRN host environment variables are passed to standalone plugin processes; v0.21 will restrict the
    plugin environment to an allowlist (...) code=plugin.standalone_env_passed
    env_names=["AWS_ACCESS_KEY_ID","AWS_SECRET_ACCESS_KEY","DATABASE_URL"] docs=docs/operations/standalone-plugin-environment.md
```

`conduit doctor` reports the same condition as a `warn` finding named `plugins.standalone_env`
with the code `plugin.standalone_env_passed`. Doctor counts any non-directory file in
`connectors.path`, so a stray file there can produce the finding where the engine, which counts
plugins that loaded, stays quiet. Run it with:

```bash
conduit doctor --check plugins.standalone_env --json
```

The warning is logged once per process, not once per plugin. It lists variable **names** only.
Values are never logged.

## What is listed

Names of host environment variables that standalone plugins will receive and that look like
credentials. Matching is case-insensitive on every OS. A name is listed when it:

- contains `PASSWORD`, `PASSWD`, `PASSPHRASE`, `TOKEN` or `SECRET` anywhere;
- starts with `AWS_`, `AZURE_`, `GOOGLE_` or `GCP_`;
- ends with `_KEY`, `_CREDENTIAL`, `_CREDENTIALS`, `_CREDS`, `_AUTH`, `_PASS`, `_PW`, `_PWD`, `_DSN`,
  `_PAT`, `APIKEY`, `APIKEYS`, `API_KEYS`, `PRIVATEKEY`, `_JWT`, `AUTHORIZATION`, `BASIC_AUTH_USER_INFO`
  (the Confluent schema registry credential), `_CONNECTION_STRING`, `_JAAS_CONFIG`, `DATABASE_URL`,
  `DATABASE_URI`, `DB_URL`, `DB_URI`, `JDBC_URL`, `CONNECTION_URL`, `BROKER_URL`, `BROKER_URI`,
  `WEBHOOK_URL` or `WEBHOOK_URI`;
- ends with `_URL` or `_URI` and names a database or broker: it contains `REDIS`, `MONGO`,
  `AMQP`, `RABBITMQ`, `KAFKA`, `POSTGRES`, `MYSQL`, `MARIADB`, `MSSQL`, `COCKROACH`, `CLICKHOUSE`,
  `ELASTIC`, `OPENSEARCH`, `CASSANDRA` or `MEMCACHE`, or has `PG` or `NATS` as a whole
  `_`-separated word (`PG_URL`, `NATS_URL`);
- is exactly `PGURI`, `PGURL`, `PGDSN`, `JWT`, `PROXY`, `DOCKER_AUTH_CONFIG`, `PIP_INDEX_URL`,
  `PIP_EXTRA_INDEX_URL`, `UV_INDEX_URL` or `UV_EXTRA_INDEX_URL`, or ends with `_PROXY`
  (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `FTP_PROXY`, `SOCKS_PROXY`; proxy URLs can embed
  credentials).

Boolean switches are not listed: a name of the form `<WORD>_AUTH` or `<WORD>_PROXY` where `<WORD>` is
`NO`, `SKIP`, `DISABLE`, `ENABLE`, `USE`, `REQUIRE`, `TRUST` or `BEHIND` (`SKIP_AUTH`, `USE_PROXY`).

The other libpq variables (`PGHOST`, `PGUSER`, `PGPORT`, `PGDATABASE`, `PGSSLMODE`, `PGPASSFILE`,
`PGSSLCERT`) name a location or a setting rather than a secret and are not listed. `PGPASSWORD`
and `PGSSLPASSWORD` are, through `PASSWORD`.

`PWD`, `OLDPWD`, `TOKENIZERS_PARALLELISM` and `NO_PROXY` (a host list) are never listed.
`CONDUIT_*` names are listed only when they also match one of the rules above (for example
`CONDUIT_DB_POSTGRES_CONNECTION_STRING` or `CONDUIT_CONNECTOR_TOKEN`); `CONDUIT_LOG_LEVEL` is not.

Only names that are plain identifiers (`[A-Za-z_][A-Za-z0-9_]{0,127}`) are printed. Matching
variables with any other name are counted as "N variable(s) with non-standard names" and not
printed.

The list is advisory. Other variables are passed too; the patterns only pick out names that look
like credentials.

## Where names are shown

Full names appear in the engine's startup log and in the local `conduit doctor` output. The MCP
`doctor` tool, on every transport, reports only a count, because its result is returned to the MCP
client. Run `conduit doctor` locally for the names. The MCP server process's environment may
differ from the engine's, so its count is for that process.

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
   `CONDUIT_CONNECTORS_STANDALONE_ENV_WARNING=false`. Embedders set
   `Options.SilenceStandalonePluginEnvWarning`.
   This only silences the warning and the doctor finding. It does not change what plugins receive.

## What changes in v0.21

v0.20.2 only warns; the environment plugins receive is unchanged. v0.21 will restrict the plugin
environment to an allowlist (tracked in #3043). Plugins that read variables from the host
environment will then need those variables listed explicitly. Use the names in this warning as the
starting point for that list.

## Alerting

Alert on the log field `code=plugin.standalone_env_passed`, or on the doctor finding code of the
same name.
