
import Model, { attr, belongsTo } from '@ember-data/model';

export default class ConfigurationModel extends Model {
  @attr('string')
  connectorDirectory;

  @attr('string')
  transformDirectory;

  @attr('string')
  conduitPort;

  @attr('string')
  logRetention;

  @attr('string')
  metricsRetention;

  @attr('string')
  metricsExportEndpoint;
}
