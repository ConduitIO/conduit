# Pipeline refused after a downgrade (`pipeline.settings_references_unsupported`)

Conduit v0.21 stores a setting that came from an environment variable (`${VAR}`) as the
template text, under a `SettingRefs` field on the connector, processor or dead-letter-queue
record, and removes the key from `Settings`. A v0.20.x binary does not understand that field.
Started anyway, the pipeline would run with those settings absent, and an optional credential
would fall back silently: the S3 connector would use the default AWS credential chain, a Kafka
connector would connect without SASL.

From v0.20.2, Conduit refuses to start such a pipeline instead. Releases v0.20.0 and v0.20.1 do
not have this check.

## Symptom

- The pipeline status is `degraded`. `State.error` (API, `conduit pipelines describe`, UI)
  starts with `pipeline.settings_references_unsupported:` and names the pipeline, the connector
  or processor, the config path (for example `/connectors/1/settings`) and the setting names.
  It never contains the template or its value.
- Starting the pipeline returns the same coded error. No connector plugin is started and
  nothing is read from the source.
- The Conduit log has a warning with the same `code`.

## Diagnosis

The database was last written by Conduit v0.21 or later and is now opened by v0.20.2. The
pipeline is file-provisioned, and the v0.20 reconcile either failed for it (check the
`provisioning failed` log lines) or did not run (`pipelines.path` unset or not mounted).

## Remediation

Pick one:

1. Run v0.21 or later again. The stored records are untouched, so nothing is lost.
2. Re-provision. Mount the pipelines directory (`--pipelines.path`), fix the pipeline file so
   it validates, and restart. A successful reconcile replaces the stored config with the file's,
   with the environment variables expanded into `Settings`, and the pipeline can then be started.
   This writes the expanded values to the database, so credentials are stored in plaintext
   again, as they were before v0.21.

A degraded pipeline is not resumed automatically after a restart. Start it once it is healed.
