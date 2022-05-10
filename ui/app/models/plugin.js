import Model, { attr } from '@ember-data/model';

export default class ConnectorPluginModel extends Model {
  @attr('string')
  name;

  @attr('string')
  summary;

  @attr('string')
  description;

  @attr('string')
  author;

  @attr()
  destinationParams;

  @attr()
  sourceParams;
}
