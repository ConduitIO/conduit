import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class KafkaDestination extends ConnectorPlugin {
  static id = 'kafka-destination';

  static name = 'Kafka Destination';
  static connectorType = 'destination';
  static pluginPath = 'builtin:kafka';

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
        'acks',
        'Acknowledgements',
        'The number of acknowledgments required before considering a record written to Kafka',
        'int',
        {
          isRequired: false,
          validations: [
            {
              type: 'inclusion',
              options: {
                list: ['all', 0, 1],
              },
            },
          ],
        }
      ),
      generateBlueprint(
        'deliveryTimeout',
        'Delivery Timeout',
        'Message delivery timeout',
        'string',
        {
          isRequired: false,
        }
      ),
    ];
  }
}
