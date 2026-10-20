# JavaScript processor output rejected (`processor.js_cyclic_output`, `processor.js_output_limit_exceeded`)

The `custom.javascript` processor checks the record its `process()` function returns before it
leaves the processor. goja hands the script live Go maps and slices, so a script can store a
value inside itself, or build a structure so deep or so heavily shared that the next consumer
cannot copy it. `Record.Clone`, the encoders and the inspector all recurse, and a runaway
recursion in Go is `fatal error: stack overflow`: it cannot be recovered and ends the whole
Conduit process, not just the pipeline. The check turns that into an ordinary processor error.

## Symptom

The failing record's error carries one of two stable codes:

| Code | Cause |
| --- | --- |
| `processor.js_cyclic_output` | The returned key or payload contains itself, for example `record.Payload.After["self"] = record.Payload.After`. The message names the path where the cycle closes (`.Payload.After.self`). |
| `processor.js_output_limit_exceeded` | The returned data nests deeper than 10,000 levels, or the record as a whole (key, `Payload.Before` and `Payload.After` together) expands to more than 16,777,216 map entries and slice elements, counting a value shared by several parents once per parent. The limit is per record: three fields that are each under it can still be over it together, and the message then names the field that crossed it. |

The record is not emitted and nothing is truncated or rewritten. It fails like any other processor
error: with a DLQ threshold above 0 it is routed to the dead-letter queue and the pipeline keeps
running; otherwise the pipeline is Degraded on the classic engine and Recovering on architecture v2,
and a recovering pipeline re-reads the same record and fails again (see the poison-record note in
[avro-limit-exceeded.md](avro-limit-exceeded.md)).

## Diagnosis

1. Read the error message for the path (`.Key`, `.Payload.Before` or `.Payload.After`, then the
   field names and `[index]` positions down to the offending value).
2. Find the assignment in the script that stores an object inside itself or inside one of its
   descendants, or the loop that nests without end.
3. Shared values are fine as long as they are not their own ancestors: `a.p = x; a.q = x` passes.

## Remediation

- Copy instead of aliasing: `record.Payload.After["snapshot"] = Object.assign({}, record.Payload.After)`.
  The copy is shallow; nested objects inside it are still shared with the original, which is fine
  unless one of them contains the copy.
- For the depth and size limits, flatten the structure or drop fields you do not need. The limits
  are fixed and not configurable.
- Enable a DLQ so one bad record does not stall the pipeline.

## Known gaps

The check runs after goja has converted the script's value into Go data. Two ways to crash the
process happen inside goja, before the check can run. Both need a script written to do it: record
data cannot trigger them, only script code can.

- **A `Map` or `Set` that contains itself.** goja exports a `Map` or `Set` without checking whether
  it is already being exported, so a collection that reaches itself recurses until
  `fatal error: stack overflow`. It happens on assignment to the record or to any input
  object, not when the record is returned:

  ```js
  var s = new Set(); s.add(s);
  r.Payload.After = {s: s};   // fatal here; the same with a Map, or via r.Payload.After["s"] = s
  ```

  Plain objects and arrays are not affected (goja tracks those), and the guard above covers them.
  Until goja is fixed, do not store a `Map` or `Set` inside itself or inside one of its own
  members. A fix in goja is being tracked.
- **Extremely deep objects.** goja's own conversion of a JavaScript object into Go recurses without
  a bound; a script that builds an object around 7 million levels deep in a loop can overflow the
  stack inside goja before the check runs. That needs several gigabytes of memory.
