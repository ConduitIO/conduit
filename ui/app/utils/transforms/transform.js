export default class Transform {
  static id = '';
  static label = '';
  static description = '';

  static blueprint = [];

  static onOptions = ['key', 'payload'];

  static toObject() {
    const { id, label, description, blueprint, onOptions } = this;
    return {
      id,
      label,
      description,
      blueprint,
      onOptions,
    };
  }
}
