<!-- markdownlint-disable MD013 -->
# Test case for the pipeline recovery feature

## Test Case 01: Recovery triggered for on a DLQ write error

**Priority** (low/medium/high):

**Description**:
Recovery is triggered when there is an error writing to a DLQ. As for a normal
destination, a DLQ write error can be a temporary error that can be solved after
a retry.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
version: "2.2"
pipelines:
  - id: file-pipeline
    status: running
    name: file-pipeline
    description: dlq write error
    connectors:
      - id: chaos-src
        type: source
        plugin: standalone:chaos
        name: chaos-src
        settings:
          readMode: error
      - id: log-dst
        type: destination
        plugin: builtin:log
        log: file-dst
    dead-letter-queue:
      plugin: "builtin:postgres"
      settings:
        table: non_existing_table_so_that_dlq_fails
        url: postgresql://meroxauser:meroxapass@localhost/meroxadb?sslmode=disable
      window-size: 3
      window-nack-threshold: 2
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 02: Recovery not triggered for fatal error - processor

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered when there is an error processing a record.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 03: Recovery not triggered - graceful shutdown

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered when Conduit is shutting down gracefully (i.e. when
typing Ctrl+C in the terminal where Conduit is running, or sending a SIGINT).

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 04: Recovery not triggered - user stopped pipeline

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered if a user stops a pipeline (via the HTTP API's
`/v1/pipelines/pipeline-id/stop` endpoint).

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 05: Recovery is configured by default

**Priority** (low/medium/high):

**Description**:
Pipeline recovery is configured by default. A failing pipeline will be restarted
a number of times without any additional configuration.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
version: "2.2"
pipelines:
    - id: chaos-to-log
      status: running
      name: chaos-to-log
      description: chaos source, error on read
      connectors:
        - id: chaos-source-1
          type: source
          plugin: standalone:chaos
          name: chaos-source-1
          settings:
            readMode: error
        - id: destination1
          type: destination
          plugin: builtin:log
          name: log-destination
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 06: Recovery not triggered on malformed pipeline

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered for a malformed pipeline, e.g. when a connector is
missing.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
version: "2.2"
pipelines:
    - id: nothing-to-log
      status: running
      name: nothing-to-log
      description: no source
      connectors:
        - id: destination1
          type: destination
          plugin: builtin:log
          name: log-destination
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 07: Conduit exits with --pipelines.exit-on-degraded=true and a pipeline failing after recovery

**Priority** (low/medium/high):

**Description**: Given a Conduit instance with
`--pipelines.exit-on-degraded=true`, and a pipeline that's failing after the
maximum number of retries configured, Conduit should shut down gracefully.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 08: Conduit doesn't exit with --pipelines.exit-on-degraded=true and a pipeline that recovers after a few retries

**Priority** (low/medium/high):

**Description**:
Given a Conduit instance with `--pipelines.exit-on-degraded=true`, and a
pipeline that recovers after a few retries, Conduit should still be running.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 09: Conduit exits with --pipelines.exit-on-degraded=true, --pipelines.error-recovery.max-retries=0, and a degraded pipeline

**Priority** (low/medium/high):

**Description**:
Given a Conduit instance with
`--pipelines.exit-on-degraded=true --pipelines.error-recovery.max-retries=0`,
and a pipeline that goes into a degraded state, the Conduit instance will
gracefully shut down. This is due `max-retries=0` disabling the recovery.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
```

**Steps**:

**Expected Result**:

**Additional comments**:

---

## Test Case 10: Recovery not triggered for fatal error - DLQ threshold exceeded

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered when the DLQ threshold is exceeded.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
version: "2.2"
pipelines:
  - id: pipeline1
    status: running
    name: pipeline1
    description: chaos destination with write errors, DLQ threshold specified
    connectors:
      - id: generator-src
        type: source
        plugin: builtin:generator
        name: generator-src
        settings:
          format.type: structured
          format.options.id: int
          format.options.name: string
          rate: "1"
      - id: chaos-destination-1
        type: destination
        plugin: standalone:chaos
        name: chaos-destination-1
        settings:
          writeMode: error
    dead-letter-queue:
      window-size: 2
      window-nack-threshold: 1
```

**Steps**:

**Expected Result**:

**Additional comments**:

---