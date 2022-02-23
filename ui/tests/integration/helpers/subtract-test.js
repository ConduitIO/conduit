import { module, test } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';

module('Integration | Helper | subtract', function (hooks) {
  setupRenderingTest(hooks);

  test('it subtracts two values', async function (assert) {
    this.set('x', 4);
    this.set('y', 2);

    await render(hbs`{{subtract this.x this.y}}`);

    assert.strictEqual(this.element.textContent.trim(), '2');
  });
});
