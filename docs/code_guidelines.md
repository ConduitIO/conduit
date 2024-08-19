# Code Guidelines

Unless specified otherwise, we should follow the guidelines outlined in
[Effective Go](https://golang.org/doc/effective_go) and
[Go Code Review Comments](https://go.dev/wiki/CodeReviewComments).

Conduit is using [golangci-lint](https://golangci-lint.run/) to ensure the code conforms to our code guidelines. Part of
the guidelines are outlined below.

## General

General pointers around writing code for Conduit:

- Functions should generally **return a specific type instead of an interface**. The caller should have the ability to
  know the exact type of the returned object and not only the interface it fulfills.
- **Interfaces should be defined locally** in the package that is using the interface and not in the package that
  defines structs which implement it. Interfaces should also be defined as minimally as possible. This way mocks can be
  generated and used independently in each package.
- Try to cleanly **separate concerns** and do not let implementation details spill to the caller code.
- When naming types, always keep in mind that the type will be used with the package name. We should **write code that
  does not stutter** (e.g. use `connector.Create` instead of `connector.CreateConnector`).
- **Pointer vs value semantics** - when defining a type, we should decide what semantic will be used for that type and
  stick with it throughout the codebase.
- **Avoid global state**, rather pass things explicitly between structs and functions.

## Packages

We generally follow the [Style guideline for Go packages](https://rakyll.org/style-packages/). Here is a short summary
of those guidelines:

- Organize code into packages by their functional responsibility.
- Package names should be lowercase only (don't use snake_case or camelCase).
- Package names should be short, but should be unique and representative. Avoid overly broad package names like `common`
  and `util`.
- Use singular package names (e.g. `transform` instead of `transforms`).
- Use `doc.go` to document a package.

Additionally, we encourage the usage of the `internal` package to hide complex internal implementation details of a
package and enforce a better separation of concerns between packages.

## Logging

We want to keep our logs as minimal as possible and reserve them for actionable messages like warnings and errors when
something does not go as expected. Info logs are fine when booting up the app, all other successful operations should be
executed silently or logged with level debug. Do not use logs for signaling normal operation, rather use metrics for
that.

Logs should contain contextual information (e.g. what triggered the action that printed a log). Our internal logger
takes care of enriching the log message using the supplied `context.Context`. There are 3 use cases:

- If the operation was triggered by a request, the log will contain the request ID.
- If the operation was triggered by a new record flowing through the pipeline, the log will contain the record position.
- If the operation was triggered by a background job, the log will contain the name / identifier of that job.

Connector plugins are free to use any logger, as long as the output is routed to stdout. Conduit will capture those logs
and display them alongside internal logs.

## Error Handling

- All errors need to be wrapped before they cross package boundaries. Wrapping is done using the `cerrors` Conduit library,
i.e. `cerrors.Errorf("could not do X: %w", err)`. We are using the same library to unwrap and compare errors,
i.e. `cerrors.Is(err, MyErrorType)`.
- Any error needs to be handled by either logging the error and recovering from it, or
wrapping and returning it to the caller. We should never both log and return the error.
- It's preferred to have a single file called `errors.go` per package which contains all the
error variables from that package.

## Testing

We have 3 test suites:

- Unit tests are normal Go tests that don't need any external services to run and mock internal dependencies. They are
  located in files named `${FILE}_test.go`, where `${FILE}.go` contains the code that's being tested.
- Integration tests are also written in Go, but can expect external dependencies (e.g. a running database instance).
  These tests should mostly be contained to code that directly communicates with those external dependencies.
  Integration tests are located in files named `${FILE}_integration_test.go`, where `${FILE}.go` contains the code
  that's being tested. Files that contain integration tests must contain the build tag `//go:build integration`.
- End-to-end tests are tests that spin up an instance of Conduit and test its operation as a black-box through the
  exposed APIs. These tests are located in the `e2e` folder.

## Documentation

We should write in-line documentation that can be read by `godoc`. This means that exported types, functions and
variables need to have a preceding comment that starts with the name of the expression and end with a dot.
