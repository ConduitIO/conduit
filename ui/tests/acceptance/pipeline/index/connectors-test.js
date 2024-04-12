import { module, test } from 'qunit';
import { visit, click, fillIn } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorModalNameInput: '[data-test-connector-modal-input="name"]',
  connectorModalPluginSelect: {
    select: '[data-test-connector-modal-select="connector-plugin"]',
    option: '[data-test-select-option-button="builtin:file"]',
  },

  connectorModalConfigFields: '[data-test-config-field]',

  connectorNodeName: '[data-test-connector-node-name]',
  connectorNodePluginName: '[data-test-connector-node-plugin-name]',

  connectorSlidePanel: '[data-test-connector-slide-panel]',

  connectorSlidePanelDropdownTrigger:
    "[data-test-dropdown-trigger='connector-panel-options']",

  connectorSlidePanelDropdownEditButton:
    '[data-test-dropdown-button="connector-panel-edit"]',
  connectorSlidePanelDropdownDeleteButton:
    '[data-test-dropdown-button="connector-panel-delete"]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',
  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',

  confirmInput: '[data-test-confirm-input]',
  confirmSubmit: '[data-test-confirm-submit-button]',
};

module('Acceptance | pipeline/index/connectors-test', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  module('with a pipeline with no connectors', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withFilePlugins');
      this.set('pipeline', pipeline);

      await visit(`/pipelines/${pipeline.id}`);
    });

    module('adding a new connector', function (hooks) {
      hooks.beforeEach(async function () {
        await click(page.pipelineAddNewNodeButton);
        await click(page.pipelineAddNewSourceButton);
        await fillIn(page.connectorModalNameInput, 'Titan Connector');
        await click(page.connectorModalPluginSelect.select);
        await click(page.connectorModalPluginSelect.option);

        const configFields = document.querySelectorAll(
          page.connectorModalConfigFields,
        );

        await fillIn(configFields[0], 'path/to/file.eren');

        await click('[data-test-connector-modal-create-button]');
      });

      test('it creates the connector', async function (assert) {
        assert
          .dom('[data-test-connector-node="source-titan-connector"]')
          .exists();
        assert.dom(page.connectorNodeName).containsText('Titan Connector');
        assert.dom(page.connectorNodePluginName).containsText('builtin:file');
      });

      module('then editing a new connector', function (hooks) {
        hooks.beforeEach(async function () {
          await click(
            '[data-test-connector-node="source-titan-connector"] > div',
          );
          await click(page.connectorSlidePanelDropdownTrigger);
          await click(page.connectorSlidePanelDropdownEditButton);
        });

        test('it retains all the current values', function (assert) {
          const configFields = document.querySelectorAll(
            page.connectorModalConfigFields,
          );

          assert.dom(page.connectorModalNameInput).hasValue('Titan Connector');
          assert.dom(configFields[0]).hasValue('path/to/file.eren');
        });
      });

      module('then deleting a new connector', function (hooks) {
        hooks.beforeEach(async function () {
          await click(
            '[data-test-connector-node="source-titan-connector"] > div',
          );
          await click(page.connectorSlidePanelDropdownTrigger);
          await click(page.connectorSlidePanelDropdownDeleteButton);
          await fillIn(page.confirmInput, 'Titan Connector');
          await click(page.confirmSubmit);
        });

        test('it removes the connector from the editor', function (assert) {
          assert.dom('[data-test-pipeline-zero-state]').exists();
          assert
            .dom('[data-test-connector-node="source-titan-connector"]')
            .doesNotExist();
        });
      });
    });
  });
});
