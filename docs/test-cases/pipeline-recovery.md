<!-- markdownlint-disable MD013 -->
# Test case for the pipeline recovery feature

## Test Case 01: Recovery not triggered for fatal error - DLQ

**Priority** (low/medium/high):

**Description**:
Recovery is not triggered when there is an error writing to a DLQ.

**Automated** (yes/no)

**Setup**:

**Pipeline configuration file**:

```yaml
version: "2.2"
pipelines:
    - id: file-pipeline
      status: running
      name: file-pipeline
      description: test pipline
      connectors:
        - id: source-connector
          type: source
          plugin: builtin:file
          name: source-connector
          settings:
            path: "/tmp/file-destination.txt"
        - id: destination-connector
          type: destination
          plugin: builtin:file
          name: destination-connector
          settings:
            path: "/tmp/file-destination.txt"
      dead-letter-queue:
        plugin: "builtin:postgres"
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
