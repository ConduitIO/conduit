import Model, { attr, belongsTo, hasMany } from '@ember-data/model';

export default class ConnectorModel extends Model {
  @attr()
  state;

  @attr()
  config;

  @attr('string')
  type;

  @belongsTo('pipeline')
  pipeline;

  @hasMany('processor')
  processors;

  @belongsTo('plugin')
  plugin;

  get name() {
    return this.config.name;
  }

  set name(newName) {
    this.config.name = newName;
  }
}
