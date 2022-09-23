import { assert, module, test } from 'qunit';
import { visit, click, fillIn, currentURL } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';
import { Response } from 'ember-cli-mirage';

const page = {
  pipelineDropdownTrigger: '[data-test-dropdown-trigger="pipeline-list-item"]',
  pipelineDropdownDeleteButton: '[data-test-dropdown-button="delete-pipeline"]',

  pipelineListItem: '[data-test-pipeline-list-item]',

  confirmInput: '[data-test-confirm-input]',
  confirmSubmit: '[data-test-confirm-submit-button]',
};

module('Acceptance | pipelines/index', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  module('deleting a pipeline that is running', function (hooks) {
    hooks.beforeEach(async function () {
      this.pipeline = this.server.create(
        'pipeline',
        { state: { status: 'STATUS_RUNNING' } },
        'withFileConnectors'
      );

      this.server.delete('/pipelines/:id', function () {
        return new Response(
          400,
          {},
          {
            code: 9,
            message: 'failed to delete pipeline: pipeline is running',
            details: [],
          }
        );
      });

      await visit(`/pipelines`);
      await click(page.pipelineDropdownTrigger);
      await click(page.pipelineDropdownDeleteButton);
      await fillIn(page.confirmInput, 'My Conduit Pipeline 1');
      await click(page.confirmSubmit);
    });

    test('fails to delete and still allows the user to view the pipeline', async function () {
      await click(page.pipelineListItem);
      assert.strictEqual(currentURL(), `/pipelines/${this.pipeline.id}`);
    });
  });
});
