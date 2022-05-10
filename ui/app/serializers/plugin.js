import ApplicationSerializer from './application';

export default class ConnectorPluginSerializer extends ApplicationSerializer {
  primaryKey = 'name';
  // pushPayload(store, payload) {
  //   const typeClass = store.modelFor('connector-plugin');
  //   let data = {};
  //   if (isArray(payload)) {
  //     data = this.normalizeResponse(store, typeClass, payload, null, 'findAll');
  //   } else {
  //     data = this.normalizeResponse(
  //       store,
  //       typeClass,
  //       payload,
  //       payload.id,
  //       'findRecord'
  //     );
  //   }

  //   store.push(data);
  // }
}
