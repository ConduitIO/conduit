import ApplicationSerializer from './application';
import { isArray } from '@ember/array';

export default class TransformSerializer extends ApplicationSerializer {
  pushPayload(store, payload) {
    const typeClass = store.modelFor('transform');
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

  normalize(typeClass, hash) {
    hash.blueprint = Object.keys(hash.blueprint).reduce((acc, item) => {
      const replaced = item.replace('.', ':');
      acc[replaced] = hash.blueprint[item];

      return acc;
    }, {});

    return super.normalize(typeClass, hash);
  }
}
