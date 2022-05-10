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

  getParams(type) {
    if (type === 'source') {
      return this.sourceParams;
    }
    if (type === 'destination') {
      return this.destinationParams;
    }
    return {};
  }
}
