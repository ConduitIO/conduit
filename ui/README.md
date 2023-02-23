# Conduit UI

Conduit UI is the web front-end for Conduit built with [Ember](https://emberjs.com/).

## Architecture

The UI application is a standard Ember application that is simply rooted in the `ui` directory of the Conduit project. The UI can then be built and embedded into Conduit's binary during a Conduit build.

## Prerequisites

You will need the following things properly installed on your computer.

- [Git](https://git-scm.com/)
- [Node.js](https://nodejs.org/)
- [Yarn](https://yarnpkg.com/)
- [Ember CLI](https://ember-cli.com/)

## Installation

- `git clone git@github.com:ConduitIO/conduit.git` the Conduit repository
- `cd conduit`
- `make ui-dependencies`

_Note:_ Commands in this readme are run from the Conduit project root. Alternatively, you can change into the `ui/` directory to directly use Yarn, [Ember CLI](https://ember-cli.com/), or non-prefixed [UI Makefile](Makefile) commands

## Running / Development

Before running Conduit UI, you must make sure the Conduit server is running

- `make run`

Alternatively, if you'd like to develop the UI against the Conduit server binary

- `make build`
- `./conduit` to run the built binary server

After confirming that Conduit server is running locally, you can now run the UI

- `make ui-server`
- Visit your app at [http://localhost:4200](http://localhost:4200).

### Running Tests

- `make ui-test`

### Linting
- `make ui-lint`
- `make ui-lint-fix`

### Building Conduit UI

Conduit UI is built and embedded into the server's binary using [Go embed directives](https://pkg.go.dev/embed). To build the binary with the embedded UI

- `make build-with-ui`

This will build the production UI asset bundle, output it to `pkg/web/ui/dist`, and build the server binary embedded with the bundle.

## Further Reading / Useful Links

- [ember.js](https://emberjs.com/)
- [ember-cli](https://ember-cli.com/)
- Development Browser Extensions
  - [ember inspector for chrome](https://chrome.google.com/webstore/detail/ember-inspector/bmdblncegkenkacieihfhpjfppoconhi)
  - [ember inspector for firefox](https://addons.mozilla.org/en-US/firefox/addon/ember-inspector/)
