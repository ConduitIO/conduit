import Model, { attr } from "@ember-data/model";

export default class ConnectorPluginModel extends Model {
  @attr("string")
  name;

  @attr("string")
  connectorType;

  @attr("string")
  pluginPath;

  @attr()
  blueprint;
}
