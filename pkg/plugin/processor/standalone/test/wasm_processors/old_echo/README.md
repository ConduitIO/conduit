# old_echo

A frozen WASM processor built against **conduit-processor-sdk v0.8.0** and
**conduit-commons v0.8.2**, before the `structured_values_v2` capability
existed. Its `Specify.Response` therefore lacks the `structured_values_v2`
field, so the host reads the capability as `false` and keeps sending it
`opencdc.v1` records (which refuse integers outside ±(2^53−1)).

It is the identity/echo processor (same source as `../echo/processor.go`), so a
non-capable guest round-trips records through the v1 path. It is committed
rather than rebuilt at test time precisely because it must stay old: rebuilding
it against the current SDK would make it declare the v2 capability and defeat
the backward-compatibility test.

There is deliberately no `processor.go` in this directory, so
`build-test-processors.sh` skips it.

Reproduce it (from a checkout whose `go.mod` pins conduit-processor-sdk
v0.8.0) with:

```sh
cd ../echo
GOOS=wasip1 GOARCH=wasm go build -ldflags="-s -w" -o ../old_echo/processor.wasm processor.go
```
