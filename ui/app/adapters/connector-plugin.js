import JSONAPIAdapter from '@ember-data/adapter/json-api';

export default class ConnectorPluginAdapter extends JSONAPIAdapter {
  namespace = 'v1';
}
