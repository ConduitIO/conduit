import Model, { attr } from '@ember-data/model';

export default class TransformModel extends Model {
  @attr('string')
  label;

  @attr('string')
  description;

  @attr
  onOptions;

  @attr
  blueprint;
}
