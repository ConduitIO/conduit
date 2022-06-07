import Transform from './transform';

export default class MaskField extends Transform {
  static id = 'maskfield';
  static label = 'MaskField';
  static description =
    'Replace field with a valid null value for the type or custom replacement.';

  static blueprint = {
    field: {
      default: '',
      description: 'Field name',
      type: 'TYPE_STRING',
      validations: [],
    },
    replacement: {
      default: '',
      description: 'Replacement value',
      type: 'TYPE_STRING',
      validations: [],
    },
  };
}
