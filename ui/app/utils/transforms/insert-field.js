import Transform from './transform';

export default class MaskField extends Transform {
  static id = 'insertfield';
  static label = 'InsertField';
  static description = '';

  static blueprint = [
    {
      id: 'static.field',
      label: 'Static field',
      placeholder: 'Enter static field',
      type: 'string',
      validationType: 'string',
      validations: [],
    },

    {
      id: 'static.value',
      label: 'Static value',
      placeholder: 'Enter replacement value',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
    {
      id: 'timestamp.field',
      label: 'Timestamp field',
      placeholder: 'Enter timestamp field',
      type: 'string',
      validationType: 'string',
      validations: [],
    },

    {
      id: 'position.field',
      label: 'Position field',
      placeholder: 'Enter position field',
      type: 'string',
      validationType: 'string',
      validations: [],
    },
  ];
}
