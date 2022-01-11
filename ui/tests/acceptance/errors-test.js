import { module, test } from 'qunit';
import { click, fillIn, findAll, visit, waitUntil } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { Response } from 'miragejs';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorModalNameInput: '[data-test-connector-modal-input="name"]',
  connectorModalPluginSelect: {
    select: '[data-test-connector-modal-select="connector-plugin"]',
    option: '[data-test-select-option-button="File Source"]',
  },

  connectorModalConfigFields: '[data-test-config-field]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',
  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',
};

module('Acceptance | error', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  module('performing an api action that errors', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');
      this.set('pipeline', pipeline);
      this.server.post('/connectors', function () {
        return new Response(
          500,
          {},
          { code: 500, message: 'Internal server error' }
        );
      });

      await visit(`/pipelines/${pipeline.id}`);
      await click(page.pipelineAddNewNodeButton);
      await click(page.pipelineAddNewSourceButton);
      await fillIn(page.connectorModalNameInput, 'Sleep Token Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.option);
      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'path/to/file.sundown');

      await click('[data-test-connector-modal-create-button]');
    });

    test('it propagates the error to the user', function (assert) {
      assert
        .dom('[data-test-error-title]')
        .containsText('Internal server error');
    });

    test('it allows the user to dismiss the error message', async function (assert) {
      assert.dom('[data-test-flash-message="Error"]').exists();

      await click('[data-test-error-dismiss]');
      await waitUntil(
        () => findAll('[data-test-flash-message="Error"]').length < 1
      );

      assert.dom('[data-test-flash-message="Error"]').doesNotExist();
    });
  });
});
