# JavaScript processor errors and record data

The `custom.javascript` processor reports the exception of a failed `process()` call as the
record's error. Errors reach the dead-letter queue, the API and the CLI output, so they must not
carry record data.

## What the error contains

- The exception type (`TypeError`, `RangeError`, `SyntaxError`, `ReferenceError`, ...), and the line
  and column in your script. A thrown object whose `name` is not one of the known exception types is
  reported as `Error`.
- The message, only when it is on an allowlist of messages that are known to hold no record data.
  Some of them name a value or a property of the record, for example `Value is not an object:
  <the value>` or `Cannot read property '<name>' of undefined`; the processor keeps the template and
  replaces that part with `<value>` or `<name>`, so
  `TypeError: Value is not an object: <value> (at line 1, column 64)` says what went wrong and where,
  not what the data was.
- Any other message is replaced by `message withheld (may contain record data)`. The goja engine
  builds many messages around a value of the record (`JSON.parse`, `Object.create`, `toFixed`,
  `eval`, ...), and a message your script throws cannot be told apart from them, so the allowlist
  errs on the side of withholding.
- An unknown `Operation` set by the script is reported by its length, not its text.

## Debugging a failing script

Use `logger` in a non-production pipeline to see what the script was working on, and remember that
its output goes to the Conduit logs. Throwing an error with a descriptive message does not help in
the record's error: the message is withheld.

```js
// the message is withheld in the record's error; the type and position are kept
throw new Error("order has no customer id");
```
