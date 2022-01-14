import { module, test } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';

module('Integration | Helper | add-one', function (hooks) {
  setupRenderingTest(hooks);

  test('it adds one', async function (assert) {
    this.set('inputValue', 1);

    await render(hbs`{{add-one this.inputValue}}`);

    assert.strictEqual(this.element.textContent.trim(), '2');
  });
});
