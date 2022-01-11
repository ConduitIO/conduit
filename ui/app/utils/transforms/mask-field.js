import Transform from './transform';

export default class MaskField extends Transform {
  static id = 'maskfield';
  static label = 'MaskField';
  static description =
    'Replace field with a valid null value for the type or custom replacement.';

  static blueprint = [
    {
      id: 'field',
      label: 'Field',
      placeholder: 'Enter field name',
      type: 'string',
      validationType: 'string',
      validations: [],
    },

    {
      id: 'replacement',
      label: 'Replacement',
      placeholder: 'Enter replacement value',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
  ];
}
