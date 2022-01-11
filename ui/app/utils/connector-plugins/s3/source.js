import ConnectorPlugin from '../connector-plugin';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default class S3Source extends ConnectorPlugin {
  static id = 's3-source';

  static name = 'S3 Source';
  static connectorType = 'source';
  static pluginPath = 'pkg/plugins/s3/s3';

  static get blueprint() {
    return [
      generateBlueprint(
        'aws_access-key-id',
        'AWS Access Key',
        'Access Key',
        'string',
        { isRequired: true }
      ),
      generateBlueprint(
        'aws_secret-access-key',
        'AWS Secret Key',
        'Secret Key',
        'string',
        { isRequired: true }
      ),

      generateBlueprint('aws_region', 'AWS Region', 'Region', 'string', {
        isRequired: true,
        validations: [
          {
            type: 'inclusion',
            options: {
              list: [
                'us-east-1',
                'us-east-2',
                'us-west-1',
                'us-west-2',
                'us-iso-east-1',
                'us-isob-east-1',
                'us-gov-east-1',
                'us-gov-west-1',
                'ap-east-1',
                'ap-northeast-1',
                'ap-northeast-2',
                'ap-south-1',
                'ap-southeast-1',
                'ap-southeast-2',
                'ca-central-1',
                'eu-central-1',
                'eu-north-1',
                'eu-west-1',
                'eu-west-2',
                'eu-west-3',
                'me-south-1',
                'sa-east-1',
                'cn-north-1',
                'cn-northwest-1',
              ],
            },
          },
        ],
      }),

      generateBlueprint('aws_bucket', 'AWS S3 Bucket', 'S3 Bucket', 'string', {
        isRequired: true,
      }),

      generateBlueprint(
        'polling-period',
        'Polling period',
        'The polling period',
        'string',
        {
          isRequired: false,
        }
      ),
    ];
  }
}
