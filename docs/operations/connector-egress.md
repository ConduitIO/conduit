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
- the S3 endpoint; with no `aws.endpoint` that is `s3.<aws.region>.amazonaws.com:443`, so list it.
  Virtual-hosted bucket names add a subdomain of that host that Conduit does not check separately.

A host name matches an entry by name, or, when the name is not listed, if **every** address it
resolves to is inside a listed address or CIDR for that port. The metadata refusal applies to the
resolved addresses either way. A name that cannot be resolved is allowed only if its name is listed.

An invalid entry is a startup error. The list cannot be changed through the API.

## What this does not do

- It checks the settings when the connector is configured (and on lifecycle events). A connector
  resolves the name again when it dials, so DNS rebinding after the check is possible for a name
  that is allowed by name. List addresses or CIDRs, or names you trust to resolve sanely.
- It does not intercept what a connector does after it connected: redirects, a Kafka cluster that
  advertises other brokers, a Postgres server that points elsewhere.
- Standalone connectors are separate processes the operator installed and are not checked.
- Validation requests (`POST /v1/connectors/validate`-style calls) check names only and do no DNS
  lookups, so they cannot be used to probe the network.
- The processors' outbound HTTP has its own guard: [outbound-http-egress.md](outbound-http-egress.md).
