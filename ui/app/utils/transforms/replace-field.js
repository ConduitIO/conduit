import Transform from './transform';

export default class MaskField extends Transform {
  static id = 'replacefield';
  static label = 'ReplaceField';
  static description =
    'Replace field with a valid null value for the type or custom replacement.';

  static blueprint = [
    {
      id: 'exclude',
      label: 'Exclude',
      placeholder: 'Enter exclude',
      type: 'string',
      validationType: 'string',
      validations: [],
    },

    {
      id: 'include',
      label: 'Include',
      placeholder: 'Enter include',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
    {
      id: 'rename',
      label: 'Rename',
      placeholder: 'Enter rename',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
  ];
}
