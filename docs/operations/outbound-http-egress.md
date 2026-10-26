# Outbound HTTP from built-in processors

`webhook.http`, `ollama.request`, `openai.*` and `cohere.*` open connections from the Conduit host.
In the webhook processor the URL is a template over record data, so the destination can be chosen by
a pipeline author or by a record. Conduit therefore checks every connection these processors make.

The WASM processor capability has its own, stricter allowlist model
([design](../design-documents/20260726-wasm-host-egress-capability.md)); the dial check is the same code.

## What is enforced

For every connection, including every redirect hop, after DNS resolution and immediately before
`connect(2)`, on every candidate address:

- Refused: loopback, RFC 1918 private, link-local (`169.254.0.0/16`, including the cloud metadata
  address), CGNAT (`100.64.0.0/10`, includes `100.100.100.200`), the Azure WireServer address,
  IPv6 loopback/link-local/unique-local (includes `fd00:ec2::254`), multicast and reserved ranges,
  and IPv4 addresses embedded in IPv4-mapped, IPv4-compatible, NAT64, 6to4, Teredo and
  IPv4-translated IPv6 forms.
- The check is on the resolved address, so decimal, octal and hex spellings of an IP
  (`2130706433`, `0x7f.1`), a hostname that resolves to a private address, and DNS rebinding are all
  covered. The address that was checked is the address that is dialed.
- `HTTP_PROXY`, `HTTPS_PROXY` and `ALL_PROXY` are ignored by these processors.
- At most 10 redirects, never from https to http, each hop checked like a new request.
- Public destinations are not restricted.

A refused connection fails the record with `processor.egress_blocked` (config path `url` for the
processor that has one). The message names the destination you configured, not the resolved address.

## Allowing a private destination

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `processors.egress.builtin-allow` | `CONDUIT_PROCESSORS_EGRESS_BUILTIN_ALLOW` | empty | Exact destinations the built-in processors may reach although they are private, loopback or link-local |

```yaml
processors:
  egress:
    builtin-allow:
      - http://127.0.0.1:11434      # local Ollama
      - https://ollama.internal:443 # a name that resolves to a private address
```

Entries are `host[:port]` or `scheme://host[:port]`. No wildcards, no paths, and `http://` only with an IP
literal. The match is on port plus IP, or port plus exact hostname: allowing `127.0.0.1:11434` does
not allow `127.0.0.1:6379`. A malformed entry fails startup. This key is separate from
`processors.egress.allow`, which only governs WASM processors.

## Migrating

- Ollama on the same host or a private network needs a `builtin-allow` entry.
- Webhooks to internal services (`http://service.internal/...`, `http://10.0.0.5/...`) need one.
- If your environment forces outbound traffic through a proxy, the built-in processors can no longer
  use it. Route at the network level (for example a transparent proxy or egress firewall).

To find what a deployment calls, look for `processor.egress_blocked` in the logs after upgrading.
