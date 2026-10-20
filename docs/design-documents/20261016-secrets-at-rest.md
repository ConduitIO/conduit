# Secrets at rest

## Summary

Stop persisting the expanded value of a `${VAR}` reference. Conduit keeps the
reference itself (`${VAR}`) in the stored pipeline config and resolves it from
the environment each time the pipeline is provisioned. Nothing secret is ever
written to badger, SQLite, Postgres, or their backups. API redaction of secret
values (v0.20.1) is unchanged.

## Context

Pipeline config authors write secrets as environment references:

```yaml
settings:
  aws.secret: ${TEST_PARSER_AWS_SECRET}
```

Today the YAML parser expands every scalar with `os.ExpandEnv`
(`pkg/provisioning/config/yaml/hooks.go`), so the _expanded_ secret is what the
provisioner persists. A database backup therefore contains the secret in
plaintext, and anyone with read access to the store sees it.

The v0.20.1 redaction work redacts secret _values_ over the API (`"***"`), but
that is presentation-only: the plaintext is still on disk.

External secret stores (Vault, KMS, Kubernetes) are explicitly out of scope for
v0.21 and are the natural v0.22 continuation — the reference stored here is
exactly what a later store resolves.

## Constraints

- **No behaviour change for `${VAR}` users.** A config that references an env
  var must keep working, and the resolved value must be what reaches the
  connector.
- **Stored-format change.** Existing databases hold the expanded value. There
  must be a migration or a documented re-provision path.
- **API redaction is preserved.** `"***"` behaviour from v0.20.1 must not
  regress.
- **Downgrade.** A binary from before this change must have a defined, non-catastrophic
  view of the new stored form.
- **No key management.** Encryption would introduce a key, its storage, rotation
  and recovery — a large security surface. The design should avoid it unless it
  is genuinely required.

## Alternatives

### A. Store the reference, resolve at runtime (chosen)

Persist `${VAR}` unexpanded. The provisioner resolves it against the process
environment when the pipeline is loaded, and stores _only_ the reference.

- A connector never sees `${VAR}`; it sees the resolved value.
- No key, no encryption, no rotation.
- The reference is the exact input a future external-store resolver consumes.

Loses: a value that is set in the environment at _creation_ time but not at
_restart_ time now fails at restart (it previously "worked" by accident because
the expanded value was persisted). This is correct — a secret that is no longer
present should fail loudly, not silently reuse a stale copy.

### B. Store encrypted with a local key

Encrypt the expanded value with a key Conduit manages (a per-install key, or a
key from the environment).

- The secret survives an env var disappearing, at the cost of key management.
- Introduces: key generation, storage, rotation, loss recovery, and a
  compromise path. Every one of those is a new failure mode with no v0.21 use
  case that needs it.

Rejected: it adds the full key-management surface to avoid the (correct) "secret
rotated away" failure, and it is superseded by external secret stores in v0.22.

### C. Keep expanding, mark values sensitive

Keep persisting the expanded value but tag it, and redact everywhere.

Rejected: the plaintext is still on disk and in backups — this does not solve
the problem the lane exists to solve, it only hides it better.

## Decision

Persist the raw `${VAR}` reference. Resolution happens at provision time against
the current environment. A pipeline whose referenced variable is missing fails
to provision with a stable error naming the variable and the config path, and
nothing is written to the store.

## Failure modes

- **Missing env var at start.** Provisioning fails with a new stable code
  (`config.env_var_missing`), naming the variable and the connector/DLQ path.
  The pipeline stays in its previous state; the error is machine-actionable.
- **Rotated secret.** The next provision reads the new value from the
  environment. No restart of anything beyond the provision cycle is needed;
  a long-running pipeline keeps the value it resolved at its last provision
  until it is re-provisioned (documented).
- **Downgrade.** A pre-change binary reads `${VAR}` literally and passes it to
  the connector. This is caught by a stored-format version: the pipeline config
  carries the new version marker, and an older binary refuses to load it with a
  "config written by a newer Conduit" error rather than sending `${VAR}` to a
  connector. The classic engine already has this version gate; the design
  reuses it.
- **A value that is itself the literal `${VAR}`.** An author who wants the
  literal string escapes it (documented escape sequence, e.g. `$${VAR}`), so a
  stored `${VAR}` is unambiguously a reference.
- **Reference in a non-secret field.** Expansion currently applies to every
  scalar. Keeping that behaviour means a reference can appear anywhere; that is
  acceptable because it is a reference, not a secret, and is resolved the same
  way.

## Upgrade / rollback

- **Upgrade.** Existing stored configs hold the expanded value. The migration
  cannot recover the original reference from an expanded value (the expansion
  is one-way), so the migration is: on upgrade, existing pipelines keep working
  from their already-expanded config, and newly provisioned (or re-provisioned)
  pipelines store references. Operators with existing secrets are directed to
  re-provision once (re-run the config with the `${VAR}` form) so the plaintext
  leaves the store; the release note and docs carry this step.
- **Rollback.** Because the stored-format version is bumped, rolling back to the
  previous binary refuses to load the new format (fail-closed), matching the
  existing version-gate behaviour. Re-provisioning with the old binary is the
  documented downgrade path.

## Observability

- The provision error names the missing variable and the config path, with a
  stable code — no guessing, no partial writes.
- Debug log at provision time records _that_ a reference was resolved (subject
  path), never the resolved value.

## Related

- #2988 (the issue), #3018 (the lane). v0.20.1 redaction: #2926.
- External secret stores (Vault/KMS/Kubernetes): v0.22, out of scope here.
