import { module, test } from 'qunit';
import { setupTest } from 'ember-qunit';

module('Unit | Serializer | connector', function (hooks) {
  setupTest(hooks);

  test('_pluginLookup with the shortest format and a single match', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres';
    let pluginsList = ['builtin:postgres@v1.0.0', 'standalone:whatever@v1.0.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the shortest format and multiple type matches returns latest standalone', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres';
    let pluginsList = [
      'builtin:postgres@v1.0.0',
      'standalone:postgres@v1.0.0',
      'standalone:whatever@v1.0.0',
    ];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'standalone:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the shortest format and multiple version matches returns latest version', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres';
    let pluginsList = [
      'builtin:postgres@v1.0.0',
      'standalone:postgres@v1.0.0',
      'standalone:postgres@v1.1.0',
    ];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'standalone:postgres@v1.1.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the version format and a single match returns the match', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres@v1.0.0';
    let pluginsList = ['builtin:postgres@v1.0.0', 'standalone:whatever@v1.0.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the version format and multiple matches returns the standalone', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres@v1.0.0';
    let pluginsList = ['builtin:postgres@v1.0.0', 'standalone:postgres@v1.0.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'standalone:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the version format and multiple matches returns the builtin when standalone isnt found', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'postgres@v1.0.0';
    let pluginsList = [
      'builtin:postgres@v1.0.0',
      'builtin:postgres@v1.1.0',
      'standalone:whatever@v1.0.0',
    ];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the type format and a single match returns the match', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'builtin:postgres';
    let pluginsList = ['builtin:postgres@v1.0.0', 'standalone:whatever@v1.0.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.0.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the type format and multiple matches returns the latest version match', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'builtin:postgres';
    let pluginsList = ['builtin:postgres@v1.0.0', 'builtin:postgres@v1.1.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.1.0',
      type: 'plugin',
    });
  });

  test('_pluginLookup with the type+version format and returns the match', function (assert) {
    let store = this.owner.lookup('service:store');
    let serializer = store.serializerFor('connector');

    let matchTo = 'builtin:postgres@v1.0.0';
    let pluginsList = ['builtin:postgres@v1.0.0', 'builtin:postgres@v1.1.0'];

    const result = serializer._pluginLookup(pluginsList, matchTo);

    assert.deepEqual(result, {
      id: 'builtin:postgres@v1.0.0',
      type: 'plugin',
    });
  });
});
