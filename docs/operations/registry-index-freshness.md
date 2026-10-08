# Registry index freshness: `registry.index_stale`

`conduit connectors install`, `conduit processor-plugins install` and `conduit connectors audit`
verify the hosted registry index (`https://registry.conduitdata.io/index.json`) before using it. One
of the checks refuses an index whose signed `payload.index.timestamp` is older than
`install.max-staleness`, which defaults to 7 days (168h). That check is what stops a CDN or mirror
from freezing clients on an old index to hide a yank.

Today the timestamp only moves when a maintainer root-signs the index by hand. If nobody does it for
7 days, every install fails for everyone. The design that removes the human from this loop is
[`20261007-registry-index-freshness.md`](../design-documents/20261007-registry-index-freshness.md).
Until it ships, this runbook is how you keep installs working.

## Symptom

Users see:

```text
code: registry.index_stale
message: index timestamp 2026-09-05T21:08:35Z is older than the max staleness window of 168h0m0s
```

Maintainers see an open issue labeled `index-staleness` in `ConduitIO/conduit-connector-registry`,
filed by `index-staleness-alarm.yml`. The alarm fires once the served index is older than 72h,
which leaves 3 to 4 days before users hit the error above.

## Diagnosis

1. Check what the CDN is actually serving:

   ```shell
   curl -s https://registry.conduitdata.io/index.json \
     | jq '{index: .payload.index, roles: [.signatures[].role]}'
   ```

2. Compare the served `index.version` with `index/index.json` on the registry repo's `main`.
   - **They match:** nobody has re-signed. This is the normal case. Go to remediation step 1.
   - **`main` is ahead of the served index:** signing worked and the deploy is broken. Check the
     recent runs of `deploy.yml` before you sign anything again. It runs daily at 06:17 UTC, so a
     failure there is usually structural, not a blip.
3. `roles` must contain `root`. An index carrying only a `freshness` signature is refused by every
   first-time client, and the site build refuses it too. If you see that, someone re-signed with the
   wrong role. Re-sign with `root`.

## Remediation

1. **Root re-sign.** In `ConduitIO/conduit-connector-registry`, dispatch `index-sign.yml` (role
   `root` is the only option), then approve the job at the `registry-signing` Environment gate. The
   job commits the re-signed index and dispatches `deploy.yml`.
2. **Confirm it is served.** Re-run the `curl` above. `index.version` should go up by one and
   `index.timestamp` should be within the last hour. Then run a real install from a clean
   connectors directory, so there's no persisted state:

   ```shell
   conduit connectors install postgres --connectors.path "$(mktemp -d)"
   ```

3. **Let the alarm close its own issue.** The next scheduled alarm run closes it once the served
   index is fresh. Don't close it by hand before that. The alarm's green run is the confirmation.

Re-sign every 5 days or so, without waiting for the alarm. The index has gone stale twice while the
alarm was firing.

### Temporary user workaround

An operator who understands the trade-off can accept an older index during an outage:

```shell
conduit connectors install postgres --install.max-staleness=336h
```

This widens the anti-freeze window for that run only (here to 14 days). Use it to get unblocked, not
as a standing setting. Every other check still runs: signatures, rollback protection, artifact
signatures and provenance.

## Related codes

- `registry.index_rollback`: the served index has a lower version than this machine has already
  verified. This is not a freshness problem. Investigate the CDN or the deploy.
- `registry.index_integrity`: a recognized key's signature didn't verify. Treat it as possible
  tampering. Do not work around it.
- `registry.trust_anchor_expired`: the index is signed by a key this build doesn't know. Upgrade
  Conduit.
