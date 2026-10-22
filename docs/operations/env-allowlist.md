# Environment variables in pipeline settings (`env.allow`)

A setting may contain `${NAME}` (or `$NAME`). Conduit replaces it with the value of the environment
variable `NAME` of the Conduit process, when the connector or processor is configured. The stored
pipeline keeps the reference, so a secret is never written to the database or a YAML file.

Settings can come from the API, so a reference is only expanded if the **operator** listed the
variable. Nothing is expanded by default.

Decision: [ADR](../architecture-decision-records/20261021-deny-by-default-settings-and-outbound-access.md).

## Setting

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `env.allow` | `CONDUIT_ENV_ALLOW` | empty | Exact variable names that `${NAME}` may expand (repeatable, or comma/space separated) |

```yaml
env:
  allow: [KAFKA_PASSWORD, S3_SECRET_KEY]
```

```sh
conduit run --env.allow KAFKA_PASSWORD --env.allow S3_SECRET_KEY
```

Names are exact: `[A-Za-z_][A-Za-z0-9_]*`. Wildcards and prefixes (`AWS_*`) are a startup error, because they
would include the very secrets this list protects. The list cannot be changed through the API.

## What is accepted

- `${NAME}` and `$NAME` for an allowed `NAME`. An allowed variable that is not set is
  `config.env_var_missing`. An empty value expands to the empty string.
- The expanded value is not expanded again. If `A` is allowed and its value is `${B}`, the setting
  becomes the text `${B}`.

## Who can read an allowed variable

Listing a variable in `env.allow` lets **anyone who can write pipeline settings through the API**
put its value into a setting that Conduit then sends somewhere, and some of those settings are shown
back. Plugin and connector errors routinely echo the expanded setting (a failed connection prints the
URL, a failed webhook prints the address), and errors appear in `GET /v1/pipelines/{id}`, in node
logs and in the DLQ metadata. So list only variables whose value an API writer may see. Do not list
variables that hold credentials of Conduit itself or of systems the pipelines do not use.

## A literal `$` in a value

There is no escape. A value that contains `$` followed by anything but a plain name (`pa$word`,
`pa$1`, `a$`) is refused as `config.env_ref_malformed`, or, where it looks like `$word`, as a
reference to a variable that is not allowed. `$$` is refused too: in pipeline YAML the parser has
already collapsed it to `$`, and through the API it is a malformed reference.

Workarounds:

- Put the whole value in an allowed environment variable and reference that: `password: ${DB_PASSWORD}`
  with `DB_PASSWORD='pa$word'`. The expanded value is never scanned again, so the `$` inside it is safe.
- In a URL use percent-encoding: `postgres://app:pa%24word@db/app`.

## Errors


| Code | Symptom | Fix |
| --- | --- | --- |
| `config.env_var_not_allowed` | `environment variable X is not in the operator allowlist (env.allow)`, config path `settings.<key>` | Add `X` to `env.allow` and restart. The error never says whether `X` is set. |
| `config.env_ref_malformed` | `malformed environment reference "${X:-y}"` | Use plain `${X}`. Defaults (`:-`, `:?`, `:+`), nesting, an unterminated `${`, and `$` followed by something that is not a name are refused. |
| `config.env_var_missing` | `environment variable X is not set` | Set `X` in the Conduit process environment and restart the pipeline. |

The error appears where the connector or processor is opened: the pipeline degrades and the error is
in the node's `stopped with error` log line and in `GET /v1/pipelines/{id}`.

## Migrating

Before upgrading, list the variables your pipelines reference:

```sh
grep -rhoE '\$\{?[A-Za-z_][A-Za-z0-9_]*\}?' pipelines/ | tr -d '${}' | sort -u
```

Add the ones that are meant to be secrets or configuration to `env.allow`. For pipelines created
through the API, read the connector and processor settings with `GET /v1/connectors` and
`GET /v1/processors` and look for `${`. A reference you do not recognise is worth a look before you allow it.

`conduit doctor` and `conduit pipelines lint` do not check `env.allow` yet.
