import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class PostgresDestination extends ConnectorPlugin {
  static id = 'postgres-destination';

  static name = 'Postgres Destination';
  static connectorType = 'destination';
  static pluginPath = 'builtin:pg';

  static get blueprint() {
    const requiredURL = generateBlueprint(
      'url',
      'Postgres DB URL',
      'Enter URL for DB',
      'string',
      { isRequired: true }
    );

    return [requiredURL];
  }
}
