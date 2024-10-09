# Connector discovery

## Connectors' location

Conduit loads standalone connectors at startup. The connector binaries need to be placed in the `connectors` directory
relative to the Conduit binary so Conduit can find them. Alternatively, the path to the standalone connectors can be
adjusted using the CLI flag `-connectors.path`, for example:

```shell
./conduit -connectors.path=/path/to/connectors/
```

Names of the connector binaries are not important, since Conduit is getting the information about connectors from
connectors themselves (using their gRPC API).

## Referencing connectors

The name used to reference a connector in API requests (e.g. to create a new connector) comes in the following format:

`[PLUGIN-TYPE:]PLUGIN-NAME[@VERSION]`

- `PLUGIN-TYPE` (`builtin`, `standalone` or `any`)
  - Defines if the specified plugin should be built-in or standalone.
  - If `any`, Conduit will use a standalone plugin if it exists and fall back to a built-in plugin.
  - Default is `any`.
- `PLUGIN-NAME`
  - Defines the name of the plugin as specified in the plugin specifications, it has to be an exact match.
- `VERSION`
  - Defines the plugin version as specified in the plugin specifications, it has to be an exact match.
  - If `latest`, Conduit will use the latest semantic version.
  - Default is `latest`.

Examples:

- `postgres`
  - will use the **latest** **standalone** **postgres** plugin
  - will fallback to the **latest** **builtin** **postgres** plugin if standalone wasn't found
- `postgres@v0.2.0`
  - will use the **standalone** **postgres** plugin with version **v0.2.0**
  - will fallback to a **builtin** **postgres** plugin with version **v0.2.0** if standalone wasn't found
- `builtin:postgres`
  - will use the **latest** **builtin** **postgres** plugin
- `standalone:postgres@v0.3.0`
  - will use the **standalone** **postgres** plugin with version **v0.3.0** (no fallback to builtin)
