import Transform from './transform';

export default class HoistField extends Transform {
  static id = 'hoistfield';
  static label = 'HoistField';
  static description = 'Hoist the field';

  static blueprint = [
    {
      id: 'field',
      label: 'Field',
      placeholder: 'Enter field name',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
  ];
}
