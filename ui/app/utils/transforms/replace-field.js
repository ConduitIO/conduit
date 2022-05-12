import Transform from './transform';

export default class MaskField extends Transform {
  static id = 'replacefield';
  static label = 'ReplaceField';
  static description =
    'Replace field with a valid null value for the type or custom replacement.';

  static blueprint = {
    exclude: {
      default: '',
      description: 'Exclude',
      type: 'TYPE_STRING',
      validations: [],
    },

    include: {
      default: '',
      description: 'Include',
      type: 'TYPE_STRING',
      validations: [],
    },

    rename: {
      default: '',
      description: 'Include',
      type: 'TYPE_STRING',
      validations: [],
    },
  };
}
