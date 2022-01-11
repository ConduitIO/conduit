import { module, test } from 'qunit';
import { setupTest } from 'ember-qunit';

module('Unit | Service | pipeline-node-manager', function(hooks) {
  setupTest(hooks);

  // TODO: Replace this with your real tests.
  test('it exists', function(assert) {
    let service = this.owner.lookup('service:pipeline-node-manager');
    assert.ok(service);
  });
});
