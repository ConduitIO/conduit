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

Listing a variable in `env.allow` lets **anyone who can write pipeline settings through the API** put
its value into a setting that Conduit then sends somewhere: the value goes to the plugin, and the plugin
may send it to the host the setting names. List only variables whose value an API writer may use that
way, and do not list variables that hold credentials of Conduit itself or of systems the pipelines do
not use.

Conduit does not hand the value back in what it reports. In every error from configuring, opening,
running or validating a connector or processor, and in the engine's own refusals (a path outside the
allowed directories, a target outside `connectors.egress.allow`), the expanded value is replaced by its
reference, `${NAME}`. That covers the API response, the node log and the pipeline error. It is a
safeguard, not a boundary: it matches the substituted text, so a plugin that transforms the value
before echoing it (hashing, encoding, truncating) is not covered, and a value shorter than three
characters is not replaced.

## A literal `$` in a value

`$$` is a literal `$` and expands nothing: `pa$$word` becomes `pa$word`, and `$${NAME}` becomes the
text `${NAME}`. A `$` that cannot start a reference (at the end of a value, or followed by a space, a
dot or punctuation, as in `price$` or `a$.`) is left as it is. Anything that looks like a reference
but is not a plain `${NAME}` / `$NAME` is refused as `config.env_ref_malformed`: `$1`, `$*`, `${A:-x}`,
an unterminated `${`.

An expanded value is never scanned again, so a `$` or `$$` inside an allowed variable's value is kept
exactly. Writing a secret that contains `$` is therefore also possible by putting it in an allowed
variable (`password: ${DB_PASSWORD}`), or percent-encoded in a URL (`pa%24word`).

**Upgrading.** Before references were restricted, `os.Expand` expanded `$$` as a variable named `$`,
which is unset, so `pa$$word` became `pa` + `word`; it is now `pa$word`. Pipeline YAML used to collapse
`$$` itself before the value was stored; it is now collapsed once, when the reference is resolved, so
`$$` means the same in YAML and through the API. Stored values with a trailing or isolated `$` keep
working as before.

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
