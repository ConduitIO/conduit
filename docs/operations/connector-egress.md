# Where built-in connectors may connect (`connectors.egress.allow`)

The Postgres connector connects to the host in its `url`, the Kafka connector to the brokers in
`servers`, the S3 connector to `aws.endpoint` (or the regional S3 endpoint).

**Who chooses the target.** Pipeline settings come from the API. Writers of the API are
authenticated and are trusted to point a connector at a database, a Kafka cluster or an S3 endpoint:
that is what creating a pipeline means. Conduit does not stop an API writer from choosing a private
host such as `10.1.2.3:5432`, because databases live there. What Conduit does is described below. If
your API writers are not trusted to choose targets, set the allowlist.

## Always on: cloud metadata endpoints

Every built-in connector refuses a target that is a cloud metadata endpoint, whether or not the
allowlist is set, and whether it is written as an address or as a name, or is what a name resolves to:

- `169.254.169.254`, the rest of the IPv4 link-local range `169.254.0.0/16` (the ECS task credentials
  endpoint `169.254.170.2` is in it), in every encoding a C library accepts (`2852039166`,
  `0xa9fea9fe`, `0251.0376.0251.0376`, `169.254.43518`);
- `fd00:ec2::/32` (AWS IPv6), `fe80::/10` (IPv6 link-local);
- the Azure WireServer `168.63.129.16` and the Alibaba Cloud endpoint `100.100.100.200`;
- `metadata.google.internal`, `metadata.goog`, `instance-data`, `instance-data.ec2.internal`,
  `metadata.tencentyun.com` and plain `metadata`;
- IPv4-mapped (`::ffff:169.254.169.254`), IPv4-compatible, NAT64 (`64:ff9b::/96` and the local-use
  `64:ff9b:1::/48`), 6to4 and Teredo forms of any of those addresses.

Private and loopback ranges are not refused here. The refusal cannot be switched off and an
allowlist entry for a metadata address is ignored. Host names are looked up when the connector is
configured and every address is checked.

Error: `connector.egress_blocked`, config path `settings.<key>` (`settings.url`, `settings.servers`,
`settings.aws.endpoint`).

## Optional: the operator allowlist

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.egress.allow` | `CONDUIT_CONNECTORS_EGRESS_ALLOW` | empty (no allowlist) | Targets built-in connectors may connect to (repeatable, or comma/space separated) |

```yaml
connectors:
  egress:
    allow:
      - db.internal:5432          # a host name and port
      - "*.rds.example.com:5432"  # any single label under the domain
      - 10.5.0.0/16:9092          # a CIDR and port
      - "[fd00::/8]:5432"         # IPv6 CIDRs and addresses go in brackets
      - minio.internal:9000
      - s3.eu-west-1.amazonaws.com:443
      - kafka.internal:*          # any port
```

When the list is set, **every** built-in connector target must match an entry:

- the Postgres hosts and ports the connection string names, including every host of a multi-host
  string and pgx's fallbacks, taken from pgx's own parse of the string and the `PG*` environment;
  a string with no host (local socket, `PGHOST`) and unix sockets are refused;
- every Kafka broker in `servers` (port 9092 when none is given);
- the S3 endpoint, but only when it can be known from the settings: `aws.endpoint` set and
  `aws.pathStyle: true`. The AWS client chooses the host itself otherwise (regional, FIPS and
  dual-stack endpoints, other partitions such as `cn` and `gov`, `AWS_ENDPOINT_URL*` variables, and in
  virtual-hosted style a subdomain named after the bucket), and a guess here could approve one host
  while the client dials another. With an allowlist set, an S3 connector without an explicit
  `aws.endpoint` and `aws.pathStyle: true` is refused (`connector.egress_blocked`, "cannot be
  checked"). Without an allowlist nothing about S3 changes except the metadata refusal.
- a Postgres connection string with no host (a local socket or `PGHOST`) is a target that cannot be
  named; with an allowlist set it is refused unless `PGHOST` names a listed host.

A host name matches an entry by name, or, when the name is not listed, if **every** address it
resolves to is inside a listed address or CIDR for that port. The metadata refusal applies to the
resolved addresses either way. A name that cannot be resolved is allowed only if its name is listed.

An invalid entry is a startup error. The list cannot be changed through the API.

## Two levels: the setting check and the dial

1. **The setting check** (what runs today). When a connector is configured, and on its lifecycle
   events, Conduit reads the targets out of the settings, looks names up, and applies the rules. It
   is an early, helpful answer, not the enforcement: it can disagree with what the client library
   later dials. Validation requests take a name-only hint that defers anything depending on what a
   name resolves to; a deferral is never treated as permission to connect.
2. **The dial guard** (`connegress.Policy.DialContext`). The rules applied at the moment of the dial:
   the name is resolved there, every address is checked (metadata addresses always; with an allowlist
   the name or all of its addresses must be listed), a target that cannot be vetted is refused, and
   the connection is made to the vetted address, so a second DNS answer cannot redirect it. Adapters
   fit it to the dial hooks of the client libraries: pgx `DialFunc`, franz-go `Dialer`, the AWS SDK's
   `http.Transport`, a MySQL driver's `DialContext`. The engine puts the policy on the call context
   of the Postgres, Kafka and S3 connectors (`connegress.PolicyFrom(ctx)`).

The engine hands the dial guard to the built-in connectors through the `conduit-commons/dialer`
seam, and a connector opens every network connection through it: data, admin, metadata, replication
and reconnect connections. This needs connector releases that read the seam (Postgres, Kafka and S3
in the built-in set); with an older connector release the dial guard is not applied to that connector
and the setting check is its only enforcement. Design:
[20261022-connector-dial-guard.md](../design-documents/20261022-connector-dial-guard.md).

A refused dial looks like a pipeline error with `connector.egress_blocked`. Typical cause: a name on
the allowlist that now resolves to an address outside it (a DNS change). Remediation: list the new
address or CIDR, or correct the DNS record. Name entries trust the name: a name listed by name may
resolve to any address except a metadata address; list CIDRs to pin addresses.

## What this does not do

- It does not intercept what a connector does after it connected: redirects, a Kafka cluster that
  advertises other brokers, a Postgres server that points elsewhere.
- **Standalone connectors are not restricted.** They are separate processes the operator installed;
  they receive no dialer and are not checked, by the allowlist or at the dial. This includes the MySQL
  connector, which is distributed standalone. The engine logs a warning at startup when the allowlist
  is set. Restricting them needs a separate mechanism (tracked with #3043).
- Validation requests check names only and do no DNS lookups, so they cannot be used to probe the
  network.
- The processors' outbound HTTP has its own guard: [outbound-http-egress.md](outbound-http-egress.md).
