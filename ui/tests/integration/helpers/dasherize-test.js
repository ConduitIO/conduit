import { module, test } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';

module('Integration | Helper | dasherize', function(hooks) {
  setupRenderingTest(hooks);

  test('it dasherizes', async function(assert) {
    this.set('inputValue', 'Woop dasherize-Me Please');

    await render(hbs`{{dasherize inputValue}}`);

    assert.equal(this.element.textContent.trim(), 'woop-dasherize-me-please');
  });
});
