import Model, { attr, belongsTo, hasMany } from '@ember-data/model';

export default class ConnectorModel extends Model {
  @attr()
  state;

  @attr()
  config;

  @attr('string')
  type;

  @attr('string')
  plugin;

  @belongsTo('pipeline')
  pipeline;

  @hasMany('processor')
  processors;

  get connectorPlugin() {
    if (this.plugin && this.type) {
      return this.store
        .peekAll('connector-plugin')
        .filterBy('pluginPath', this.plugin)
        .findBy('connectorType', this.type);
    } else {
      return null;
    }
  }

  get name() {
    return this.config.name;
  }

  set name(newName) {
    this.config.name = newName;
  }
}
