# Conduit Connector for <!-- readmegen:name --> <resource> <!-- /readmegen:name -->

[Conduit](https://conduit.io) connector for <!-- readmegen:name --> <resource> <!-- /readmegen:name -->.

<!-- readmegen:description -->
<!-- /readmegen:description -->

## Source

A source connector pulls data from an external resource and pushes it to
downstream resources via Conduit.

### Configuration

<!-- readmegen:source.parameters.yaml -->
<!-- /readmegen:source.parameters.yaml -->

## Destination

A destination connector pushes data from upstream resources to an external
resource via Conduit.

### Configuration

<!-- readmegen:destination.parameters.yaml -->
<!-- /readmegen:destination.parameters.yaml -->

## Development

- To install the required tools, run `make install-tools`.
- To generate code (mocks, re-generate `connector.yaml`, update the README,
  etc.), run `make generate`.

## How to build?

Run `make build` to build the connector.

## Testing

Run `make test` to run all the unit tests. Run `make test-integration` to run
the integration tests.

The Docker compose file at `test/docker-compose.yml` can be used to run the
required resource locally.

## How to release?

The release is done in two steps:

- Bump the version in [connector.yaml](/connector.yaml). This can be done
  with [bump_version.sh](/scripts/bump_version.sh) script, e.g.
  `scripts/bump_version.sh 2.3.4` (`2.3.4` is the new version and needs to be a
  valid semantic version). This will also automatically create a PR for the
  change.
- Tag the connector, which will kick off a release. This can be done
  with [tag.sh](/scripts/tag.sh).

## Known Issues & Limitations

- Known issue A
- Limitation A

## Planned work

- [ ] Item A
- [ ] Item B
