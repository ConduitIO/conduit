import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class FileDestination extends ConnectorPlugin {
  static id = 'file-destination';

  static name = 'File Destination';
  static connectorType = 'destination';
  static pluginPath = 'builtin:file';

  static get blueprint() {
    const requiredString = generateBlueprint(
      'path',
      'File Path',
      'Enter path to file',
      'string',
      { isRequired: true }
    );
    return [requiredString];
  }
}
