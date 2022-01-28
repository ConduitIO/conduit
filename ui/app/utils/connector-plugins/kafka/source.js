import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class KafkaSource extends ConnectorPlugin {
  static id = 'kafka-source';

  static name = 'Kafka Source';
  static connectorType = 'source';
  static pluginPath = 'pkg/plugins/kafka/kafka';

  static get blueprint() {
    return [
      generateBlueprint(
        'servers',
        'Kafka Servers',
        'A list of bootstrap servers to connect to',
        'string',
        { isRequired: true }
      ),

      generateBlueprint(
        'topic',
        'Kafka Topic',
        'The topic to which records will be written to',
        'string',
        { isRequired: true }
      ),
      generateBlueprint(
        'readFromBeginning',
        'Read from beginning',
        'Whether or not to read a topic from beginning',
        'boolean',
        { isRequired: false }
      ),
    ];
  }
}
