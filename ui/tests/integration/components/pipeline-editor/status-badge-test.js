import { module, test } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';

module(
  'Integration | Component | pipeline-editor/status-badge',
  function (hooks) {
    setupRenderingTest(hooks);

    test('it renders as running', async function (assert) {
      await render(hbs`<PipelineEditor::StatusBadge @status='running'/>`);
      assert.dom('[data-test-status-badge]').hasClass('bg-teal-600');
      assert.dom('[data-test-status-badge]').hasClass('text-white');
      assert.dom('[data-test-status-badge]').containsText('running');
    });

    test('it renders as degraded', async function (assert) {
      await render(hbs`<PipelineEditor::StatusBadge @status='degraded'/>`);
      assert.dom('[data-test-status-badge]').hasClass('bg-orange-700');
      assert.dom('[data-test-status-badge]').hasClass('text-white');
      assert.dom('[data-test-status-badge]').containsText('degraded');
    });

    test('it renders as paused', async function (assert) {
      await render(hbs`<PipelineEditor::StatusBadge @status='paused'/>`);
      assert.dom('[data-test-status-badge]').hasClass('bg-gray-500');
      assert.dom('[data-test-status-badge]').hasClass('text-white');
      assert.dom('[data-test-status-badge]').containsText('paused');
    });
  }
);
