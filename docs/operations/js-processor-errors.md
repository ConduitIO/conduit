# JavaScript processor errors and record data

The `custom.javascript` processor reports the exception of a failed `process()` call as the
record's error. Errors reach the dead-letter queue, the API and the CLI output, so they must not
carry record data.

## What the error contains

- The exception type, for example `TypeError`, the line and column in your script, and the message.
- The goja engine builds some messages around a value of the record being processed, such as
  `Value is not an object: <the value>` or `Cannot read property '<name>' of undefined`. The
  processor keeps the message template and replaces the value or the property name with `<value>` or
  `<name>`, so `TypeError: Value is not an object: <value> (at line 1, column 64)` tells you what
  went wrong and where, not what the data was.
- An unknown `Operation` set by the script is reported by its length, not its text.

## What is not changed

A message **you** throw (`throw new Error(...)`, or `throw "text"`) cannot be told from an engine
message, so it is passed through as it is, cut to 200 characters. Do not put record data in the
messages you throw:

```js
// do not do this: the key ends up in the DLQ record, the API and the logs
throw new Error("bad record " + record.Key);

// do this: say what is wrong, and where
throw new Error("order has no customer id");
```

To debug with record data, use `logger` in a non-production pipeline, and remember that its output
goes to the Conduit logs.
