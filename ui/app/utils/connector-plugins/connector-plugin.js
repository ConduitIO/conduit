export default class ConnectorPlugin {
  static id = '';
  static name = '';
  static connectorType = '';
  static pluginPath = '';

  static blueprint = [];

  static toObject() {
    const { id, name, connectorType, pluginPath, blueprint } = this;
    return {
      id,
      name,
      connectorType,
      pluginPath,
      blueprint,
    };
  }
}
