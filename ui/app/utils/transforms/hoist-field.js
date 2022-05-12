import Transform from './transform';

export default class HoistField extends Transform {
  static id = 'hoistfield';
  static label = 'HoistField';
  static description = 'Hoist the field';

  static blueprint = {
    field: {
      default: '',
      description: 'Field name',
      type: 'TYPE_STRING',
      validations: [],
    },
  };
}
