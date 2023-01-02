import Transform from './transform';

export default class HoistField extends Transform {
  static id = 'hoistfield';
  static label = 'HoistField';
  static description = 'Wrap data using a specified field name in a Map';

  static blueprint = {
    field: {
      default: '',
      description: 'Field name',
      type: 'TYPE_STRING',
      validations: [],
    },
  };
}
