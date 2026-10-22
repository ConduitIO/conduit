# Files read or written by pipeline settings

Two built-in components turn a setting into a file operation: the `file` connector (`path`) and the
`custom.javascript` processor (`script.path`, `require()`). Settings can come from the API, so both
are confined to directories the **operator** configured. Outside them, nothing is read or written.

Decision: [ADR](../architecture-decision-records/20261021-deny-by-default-settings-and-outbound-access.md).

## Settings

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.file.allowed-dirs` | `CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS` | `<data dir>/files` | Directories the file connector may read and write. Setting it replaces the default |
| `processors.javascript.allowed-dirs` | `CONDUIT_PROCESSORS_JAVASCRIPT_ALLOWED_DIRS` | empty | Directories `custom.javascript` may load `script.path` and `require()` modules from |

`<data dir>` is the working directory `conduit run` uses (the directory holding `conduit.db`).
The default is the `files` directory under it, not the data dir itself, so a pipeline cannot read
`conduit.db` or `conduit.yaml`. `script.path` defaults to `<data dir>/scripts`.

```yaml
connectors:
  file:
    allowed-dirs: [/var/lib/conduit/files, /srv/exports]
processors:
  javascript:
    allowed-dirs: [/etc/conduit/js]   # also turns require() on, limited to these directories
```

## Which settings are covered

| Plugin | Setting | Policy |
| --- | --- | --- |
| `file` (source, destination) | `path` | `connectors.file.allowed-dirs` |
| `generator` | `format.options.path`, `collections.<name>.format.options.path` (the `file` record format reads the file and uses it as every record's payload) | `connectors.file.allowed-dirs` |
| `postgres` (source, destination) | `sslcert`, `sslkey`, `sslrootcert`, `sslcrl` inside the connection string `url`, in URL form or key/value form (`sslrootcert=system` is not a file) | `secrets.allowed-dirs` |
| `postgres` (source, destination) | `passfile`, `servicefile` and `service` in `url` are **refused outright**, whatever the path: pgx reads those files as credentials and as connection settings (a service file can redirect the host and turn TLS off). When `url` does not set them, Conduit sets `passfile`, `sslcert` and `sslkey` to the empty string, so pgx does not fall back to `~/.pgpass`, `~/.postgresql/postgresql.crt` or `.key` of the user Conduit runs as | none (not accepted) |
| `custom.javascript` | `script.path`, `require()` | `processors.javascript.allowed-dirs` |

Kafka's `caCert`, `clientCert` and `clientKey` are PEM text, not paths, and S3 reads no file named by
a setting. A test (`TestBuiltinConnectorFileParams`) fails when a built-in connector gains a
parameter that looks like it names a file and is not in the table or in a reviewed non-file list, so
bumping a connector cannot silently add an uncovered file setting.

TLS and credential files get their own list, `secrets.allowed-dirs`, instead of sharing the data-file
list, because they live somewhere else (mounted secrets) and allowing those directories to the file
connector would make them readable as data. The default is `<data dir>/secrets`.

Two classes, because a CA bundle is public and a key is not:

| Parameter | May come from |
| --- | --- |
| `sslkey`, `sslcert` (the client's identity) | `secrets.allowed-dirs` only |
| `sslrootcert`, `sslcrl` (CA bundles, revocation lists) | `secrets.allowed-dirs` **and** the system CA directories: `/etc/ssl`, `/etc/pki`, `/etc/ca-certificates`, `/usr/share/ca-certificates`, `/usr/local/share/ca-certificates` |

`/run/secrets` is in neither: it holds every secret mounted into the container, so name the individual
directory you need in `secrets.allowed-dirs`. Neither includes the data dir itself (it holds
`conduit.db` and `conduit.yaml`) or the Kubernetes service account directory. Setting
`secrets.allowed-dirs` replaces `<data dir>/secrets`; the system CA directories stay available to CA
bundles.

For Postgres the path that was checked, with symlinks resolved, is written back into the connection
string, so pgx opens exactly that path: a link swapped afterwards does not redirect it.

| Key | Env | Default |
| --- | --- | --- |
| `secrets.allowed-dirs` | `CONDUIT_SECRETS_ALLOWED_DIRS` | `<data dir>/secrets` |

These defaults hold on every start path, including `conduit run` with no flags: an unset repeatable
flag does not replace them (`TestRunDefaultsFileAccess` drives the real command). A flag, the
config file or the environment replaces only its own key.

## How a path is checked

The path is made absolute and cleaned, symlinks are resolved (`filepath.EvalSymlinks`), and the result
must be inside an allowed directory that was resolved the same way. A file that does not exist yet
is checked through its nearest existing parent; a dangling symlink is followed to its target. So
`../`, a symlink out of an allowed directory, an allowed directory that is itself a symlink, and
relative paths are all judged on where they really point. `${VAR}` in a path is expanded first (it must
be in `env.allow`), and the expanded value is what is checked. The connector receives the resolved path.

The check runs when the connector or processor is configured, when a lifecycle event (created,
updated, deleted) is sent to a plugin, and on validation requests (`POST /v1/connectors/validate`
and connector creation) for a literal path, so a validation call cannot be used to probe which files
exist on the host. Lifecycle events carry the same resolved settings Configure gets; if a reference
cannot be resolved (a variable that is no longer allowed or set), the event fails with the coded
error, including the delete hook, so fix the configuration and retry the delete.

**Open time.** Where Conduit itself opens the file it enforces the policy again at `open(2)`:
`custom.javascript` (`script.path` and `require()`) opens relative to the allowed directory without
following a symlink in any component (`openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` on Linux,
one `openat` per component with `O_NOFOLLOW` elsewhere on Unix), so a symlink swapped in after the
check cannot redirect the read. The file destination does the same when the engine supplies its open
function (`file.WithOpener`, `conduit-connector-file` v0.11.0 or later). On Windows the open is the
checked path opened normally.

Not enforced at open time, because the connector or a library opens the path itself: the file
**source** (`github.com/nxadm/tail`), the generator's file format (`os.ReadFile` inside the connector)
and the Postgres certificate files (pgx). For those the check is at configure time only, and a local
user who can replace a directory component with a symlink in an allowed directory between the check
and the open can still redirect it. Keep allowed directories writable only by the Conduit user.

## `custom.javascript`

- `script.path` is read only from the allowed directories.
- `require()` is not defined unless `processors.javascript.allowed-dirs` is set. When it is, modules
  load only from those directories (absolute paths, `../`, symlinks and source maps outside them are
  refused). Only files are loaded; there are no built-in Node modules.
- An inline `script` is not affected.

## Output file mode

A destination file created by the file connector is mode 0600 (further restricted by the umask) as of
`conduit-connector-file` v0.11.0 / v0.10.11. Existing files keep their mode. If another user or
service reads the file, create it first with the mode you want.

## Errors

| Code | Symptom | Fix |
| --- | --- | --- |
| `config.path_not_allowed` | `path "..." is not allowed: the path is outside every allowed directory`, config path `settings.path` or `settings.script.path` | Move the file into an allowed directory, or add its directory to the key named in the suggestion and restart |

The path controls cover **built-in** connectors (and a standalone connector that carries the same
name as one of the plugins in the table). A standalone connector is native code running as the
Conduit user; it opens whatever its own code opens. What it inherits from Conduit's environment is
limited separately, see [standalone plugin environment](standalone-plugin-environment.md).
