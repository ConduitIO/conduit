# conduit-connector-connectorname

A Conduit connector scaffolded by `conduit connector new --lang python`. It
imports the connector SDK from `conduit.connector` (the merged `conduit`
distribution, published on PyPI as `conduitio`).

## Run the acceptance suite

```bash
uv sync --extra dev
uv run pytest -v
```

`tests/test_acceptance.py` runs the versioned SDK acceptance suite
(`conduit.connector.testing.acceptance`) against `GeneratorSource` in-process.

## Build the standalone plugin

```bash
conduit-connector-sdk build . -o connectorname.pyz
```

Conduit launches a standalone connector with a clean environment, so the
directly-executable `.pyz` artifact (absolute interpreter shebang + vendored
dependencies) is the deployment unit.

## Wire it into a pipeline

```yaml
source:
  plugin: "connectorname"
  settings:
    count: "100"
```

Replace `GeneratorSource` with your real source (or add a `Destination`) and
re-run the acceptance suite and build steps.
