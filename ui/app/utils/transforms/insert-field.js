import Transform from './transform';

export default class InsertField extends Transform {
  static id = 'insertfield';
  static label = 'InsertField';
  static description =
    'Insert fields using attributes from the record metadata or a configured static value.';

  static blueprint = {
    'static.field': {
      default: '',
      description: 'Static field',
      type: 'TYPE_STRING',
      validations: [],
    },
    'static.value': {
      default: '',
      description: 'Static value',
      type: 'TYPE_STRING',
      validations: [],
    },
    'timestamp.field': {
      default: '',
      description: 'Timestamp field',
      type: 'TYPE_STRING',
      validations: [],
    },
    'position.field': {
      default: '',
      description: 'Position field',
      type: 'TYPE_STRING',
      validations: [],
    },
  };
}
