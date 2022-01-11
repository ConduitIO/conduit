import { module, test } from 'qunit';
import { setupTest } from 'ember-qunit';

module('Unit | Controller | pipelines', function(hooks) {
  setupTest(hooks);

  // TODO: Replace this with your real tests.
  test('it exists', function(assert) {
    let controller = this.owner.lookup('controller:pipelines');
    assert.ok(controller);
  });
});
