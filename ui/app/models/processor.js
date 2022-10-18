import Model, { attr, belongsTo } from '@ember-data/model';

export default class ProcessorModel extends Model {
  @attr()
  type;

  @attr()
  config;

  @attr()
  parent;

  @belongsTo('connector')
  connector;

  get transform() {
    if (this.type) {
      return this.store.peekAll('transform').find((transform) => {
        return transform.onOptions.find((onOption) => {
          return `${transform.id}${onOption}` === this.type;
        });
      });
    } else {
      return null;
    }
  }

  get onOption() {
    if (this.transform) {
      return this.transform.onOptions.find((onOption) => {
        return `${this.transform.id}${onOption}` === this.type;
      });
    } else {
      return null;
    }
  }
}
