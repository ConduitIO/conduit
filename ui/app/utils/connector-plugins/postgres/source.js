import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class PostgresSource extends ConnectorPlugin {
  static id = 'postgres-source';

  static name = 'Postgres Source';
  static connectorType = 'source';
  static pluginPath = 'builtin:postgres';

  static get blueprint() {
    const requiredURL = generateBlueprint(
      'url',
      'Postgres Database URL',
      'Enter URL for database',
      'string',
      { isRequired: true }
    );

    const requiredTable = generateBlueprint(
      'table',
      'Postgres table name',
      'Enter a table name',
      'string',
      { isRequired: true }
    );
    return [requiredURL, requiredTable];
  }
}
