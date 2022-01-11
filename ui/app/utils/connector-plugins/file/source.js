import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class FileSource extends ConnectorPlugin {
  static id = 'file-source';

  static name = 'File Source';
  static connectorType = 'source';
  static pluginPath = 'pkg/plugins/file/file';

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
