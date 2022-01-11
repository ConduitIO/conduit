import Model, { attr, belongsTo } from '@ember-data/model';

export default class ProcessorModel extends Model {
  @attr()
  name;

  @attr()
  config;

  @attr('string', { defaultValue: 'TYPE_TRANSFORM' })
  type;

  @attr()
  parent;

  @belongsTo('connector')
  connector;

  get transform() {
    if (this.name) {
      return this.store.peekAll('transform').find((transform) => {
        return transform.onOptions.find((onOption) => {
          return `${transform.id}${onOption}` === this.name;
        });
      });
    } else {
      return null;
    }
  }

  get onOption() {
    if (this.transform) {
      return this.transform.onOptions.find((onOption) => {
        return `${this.transform.id}${onOption}` === this.name;
      });
    } else {
      return null;
    }
  }
}
