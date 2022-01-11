import ApplicationSerializer from './application';
import { isArray } from '@ember/array';

export default class ConnectorPluginSerializer extends ApplicationSerializer {
  pushPayload(store, payload) {
    const typeClass = store.modelFor('connector-plugin');
    let data = {};
    if (isArray(payload)) {
      data = this.normalizeResponse(store, typeClass, payload, null, 'findAll');
    } else {
      data = this.normalizeResponse(
        store,
        typeClass,
        payload,
        payload.id,
        'findRecord'
      );
    }

    store.push(data);
  }
}
