# Health check

Conduit’s health check can be used to determine if Conduit is running correctly. What it does is to check if Conduit 
can successfully connect to the database which it was configured with (which can be BadgerDB, PostgreSQL or the 
in-memory one). The health check is available at the `/healthz` path. Here’s an example:

```bash
$ curl "http://localhost:8080/healthz"
{"status":"SERVING"}
```

You can also check individual services within Conduit. The following example checks if the PipelineService is running:
```bash
$ curl "http://localhost:8080/healthz?service=PipelineService"
{"status":"SERVING"}
```

The services which can be checked for health are: `PipelineService`, `ConnectorService`, `ProcessorService`, and 
`PluginService`.
