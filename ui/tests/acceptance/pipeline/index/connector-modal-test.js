import { module, test } from 'qunit';
import { visit, click, fillIn } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorModalNameInput: '[data-test-connector-modal-input="name"]',
  connectorModalPluginSelect: {
    select: '[data-test-connector-modal-select="connector-plugin"]',
    sourceOption: '[data-test-select-option-button="builtin:generic"]',
    destinationOption: '[data-test-select-option-button="builtin:generic"]',
  },

  connectorModalConfigFields: '[data-test-config-field]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',

  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',
  pipelineAddNewDestinationButton:
    '[data-test-pipeline-editor-add-node-destination]',

  connectorModalOptionalTab: '[data-test-connector-modal-optional-tab]',
};

module('Acceptance | pipeline/index/connector-modal-test', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  hooks.beforeEach(function () {
    this.server.create(
      'plugin',
      'source',
      'destination',
      'withGenericBlueprint'
    );
  });

  module('creating a source', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');
      this.set('pipeline', pipeline);

      await visit(`/pipelines/${pipeline.id}`);
      await click(page.pipelineAddNewNodeButton);
      await click(page.pipelineAddNewSourceButton);
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.sourceOption);
    });
    test('shows the connectors required fields', function (assert) {
      assert.dom('[data-test-config-field="titanName"]').exists();
      assert
        .dom('[data-test-config-field="titanName"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="titan:height"]').exists();
      assert
        .dom('[data-test-config-field="titan:height"]')
        .hasAttribute('type', 'number');

      assert.dom('[data-test-config-field="titan@@type"]').exists();
      assert
        .dom('[data-test-config-field="titan@@type"]')
        .hasAttribute('type', 'text');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);

      assert.dom('[data-test-config-field="titan:founding"]').exists();
      assert
        .dom('[data-test-config-field="titan:founding"] [data-test-toggle]')
        .exists();
    });

    test('sends the connectors configuration on save', async function (assert) {
      assert.expect(2);
      const pipelineID = this.pipeline.id;
      this.server.post(
        '/connectors',
        function ({ connectors, plugins, pipelines }, request) {
          let attrs = JSON.parse(request.requestBody);

          assert.deepEqual(
            attrs,
            {
              config: {
                name: 'My Connector',
                settings: {
                  titanName: 'sleep',
                  'titan:height': '100',
                  'titan.type': 'attack',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'builtin:generic',
              type: 'TYPE_SOURCE',
            },
            'it calls the API with the correct protocol'
          );

          const plugin = plugins.find(attrs.plugin);
          const pipeline = pipelines.find(attrs.pipeline_id);

          return connectors.create({
            type: attrs.type,
            config: attrs.config,
            pipeline,
            plugin,
          });
        }
      );
      await fillIn(page.connectorModalNameInput, 'My Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.sourceOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], '100');
      await fillIn(configFields[2], 'attack');

      await click('[data-test-connector-modal-create-button]');

      assert.dom(page.connectorModalNameInput).doesNotExist();
    });
  });

  module('creating a destination', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');
      this.set('pipeline', pipeline);

      await visit(`/pipelines/${pipeline.id}`);
      await click(page.pipelineAddNewNodeButton);
      await click(page.pipelineAddNewDestinationButton);
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.destinationOption);
    });
    test('shows the connectors required fields', function (assert) {
      assert.dom('[data-test-config-field="titanName"]').exists();
      assert
        .dom('[data-test-config-field="titanName"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="titan:height"]').exists();
      assert
        .dom('[data-test-config-field="titan:height"]')
        .hasAttribute('type', 'number');

      assert.dom('[data-test-config-field="titan@@type"]').exists();
      assert
        .dom('[data-test-config-field="titan@@type"]')
        .hasAttribute('type', 'text');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);

      assert.dom('[data-test-config-field="titan:founding"]').exists();
      assert
        .dom('[data-test-config-field="titan:founding"] [data-test-toggle]')
        .exists();
    });

    test('sends the connectors configuration on save', async function (assert) {
      assert.expect(2);
      const pipelineID = this.pipeline.id;
      this.server.post(
        '/connectors',
        function ({ connectors, plugins, pipelines }, request) {
          let attrs = JSON.parse(request.requestBody);

          assert.deepEqual(
            attrs,
            {
              config: {
                name: 'My Connector',
                settings: {
                  titanName: 'sleep',
                  'titan:height': '100',
                  'titan.type': 'attack',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'builtin:generic',
              type: 'TYPE_DESTINATION',
            },
            'it calls the API with the correct protocol'
          );
          const plugin = plugins.find(attrs.plugin);
          const pipeline = pipelines.find(attrs.pipeline_id);

          return connectors.create({
            type: attrs.type,
            config: attrs.config,
            pipeline,
            plugin,
          });
        }
      );
      await fillIn(page.connectorModalNameInput, 'My Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.destinationOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], '100');
      await fillIn(configFields[2], 'attack');

      await click('[data-test-connector-modal-create-button]');

      assert.dom(page.connectorModalNameInput).doesNotExist();
    });
  });
});
