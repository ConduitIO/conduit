# Files read or written by pipeline settings

Two built-in components turn a setting into a file operation: the `file` connector (`path`) and the
`custom.javascript` processor (`script.path`, `require()`). Settings can come from the API, so both
are confined to directories the **operator** configured. Outside them, nothing is read or written.

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

## How a path is checked

The path is made absolute and cleaned, symlinks are resolved (`filepath.EvalSymlinks`), and the result
must be inside an allowed directory that was resolved the same way. A file that does not exist yet
is checked through its nearest existing parent; a dangling symlink is followed to its target. So
`../`, a symlink out of an allowed directory, an allowed directory that is itself a symlink, and
relative paths are all judged on where they really point. The connector receives the resolved path.

The check runs when the connector or processor is configured, and on validation requests (`POST /v1/connectors/validate` and connector creation) for a literal `path`, so a validation call cannot be used to probe which files exist on the host. A `path` that still holds a `${VAR}` reference is checked after expansion. It cannot stop a local user who can
modify the allowed directory from swapping a directory for a symlink afterwards, so keep allowed
directories writable by the Conduit user only.

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

Standalone connectors are not path-checked unless they are named `file`; they are installed by the
operator and run as separate processes. Other connector settings that name files, such as TLS
certificate paths, are not covered.
