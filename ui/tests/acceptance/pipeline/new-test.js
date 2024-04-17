import { module, test } from 'qunit';
import { visit, click, fillIn } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  pipelineFormNameInput: '[data-test-pipeline-form-name-input]',
  pipelineFormDescriptionInput: '[data-test-pipeline-form-description-input]',
  pipelineFormPrimaryButton: '[data-test-button="primary"]',
  pipelineFormSecondaryButton: '[data-test-button="secondary"]',
  pipelineEditorZeroState: '[data-test-pipeline-zero-state]',
};

module('Acceptance | pipeline/new', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  hooks.beforeEach(async function () {
    await visit('/pipelines/new');
  });

  module('building a pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      await fillIn(page.pipelineFormNameInput, 'Pants');
      await fillIn(
        page.pipelineFormDescriptionInput,
        'I am a pipeline description',
      );
    });

    test('allows the user to save the pipeline', function (assert) {
      assert.dom(page.pipelineFormPrimaryButton).isNotDisabled();
    });
  });

  module('when saving a valid pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      await fillIn(page.pipelineFormNameInput, 'Pants');
      await fillIn(
        page.pipelineFormDescriptionInput,
        'I am a pipeline description',
      );
      await click(page.pipelineFormPrimaryButton);
    });

    test('transitions the user to the new blank pipeline', function (assert) {
      assert.dom(page.pipelineEditorZeroState).exists();
    });
  });
});
