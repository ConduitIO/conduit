import ApplicationSerializer from './application';

export default class ConnectorPluginSerializer extends ApplicationSerializer {
  primaryKey = 'name';
}
