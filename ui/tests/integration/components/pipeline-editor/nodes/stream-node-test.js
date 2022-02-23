import { module, skip } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';

module(
  'Integration | Component | pipeline-editor/nodes/stream-node',
  function (hooks) {
    setupRenderingTest(hooks);

    skip('it renders', async function (assert) {
      // Set any properties with this.set('myProperty', 'value');
      // Handle any actions with this.set('myAction', function(val) { ... });

      await render(hbs`<PipelineEditor::Nodes::StreamNode />`);

      assert.equal(this.element.textContent.trim(), '');

      // Template block usage:
      await render(hbs`
      <PipelineEditor::Nodes::StreamNode>
        template block text
      </PipelineEditor::Nodes::StreamNode>
    `);

      assert.equal(this.element.textContent.trim(), 'template block text');
    });
  }
);
