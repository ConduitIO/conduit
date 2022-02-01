import { module, test } from 'qunit';
import { visit, click, fillIn } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorModalNameInput: '[data-test-connector-modal-input="name"]',
  connectorModalPluginSelect: {
    select: '[data-test-connector-modal-select="connector-plugin"]',
    sourceOption: '[data-test-select-option-button="Kafka Source"]',
    destinationOption: '[data-test-select-option-button="Kafka Destination"]',
  },

  connectorModalConfigFields: '[data-test-config-field]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',

  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',
  pipelineAddNewDestinationButton: '[data-test-pipeline-editor-add-node-destination]',

  connectorModalOptionalTab: '[data-test-connector-modal-optional-tab]',
};

module('Acceptance | pipeline/index/connectors/kafka', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

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
      assert.dom('[data-test-config-field="servers"]').exists();
      assert
        .dom('[data-test-config-field="servers"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="topic"]').exists();
      assert
        .dom('[data-test-config-field="topic"]')
        .hasAttribute('type', 'text');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);
      assert.dom('[data-test-config-field="readFromBeginning"]').exists();
      assert
        .dom('[data-test-config-field="readFromBeginning"] [data-test-toggle]')
        .exists();
    });

    test('sends the connectors configuration on save', async function (assert) {
      assert.expect(2);
      const pipelineID = this.pipeline.id;
      this.server.post(
        '/connectors',
        function ({ connectors }, { requestBody }) {
          let attrs = JSON.parse(requestBody);

          assert.deepEqual(
            attrs,
            {
              config: {
                name: 'My Kafka Connector',
                settings: {
                  servers: 'sleep',
                  topic: 'token',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'pkg/plugins/kafka/kafka',
              type: 'TYPE_SOURCE',
            },
            'it calls the API with the correct protocol'
          );
          return connectors.create(attrs);
        }
      );
      await fillIn(page.connectorModalNameInput, 'My Kafka Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.sourceOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], 'token');

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
      assert.dom('[data-test-config-field="servers"]').exists();
      assert
        .dom('[data-test-config-field="servers"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="topic"]').exists();
      assert
        .dom('[data-test-config-field="topic"]')
        .hasAttribute('type', 'text');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);
      assert.dom('[data-test-config-field="acks"]').exists();
      assert
        .dom('[data-test-config-field="acks"]')
        .hasAttribute('type', 'button');

      assert.dom('[data-test-config-field="deliveryTimeout"]').exists();
      assert
        .dom('[data-test-config-field="deliveryTimeout"]')
        .hasAttribute('type', 'text');
    });

    test('sends the connectors configuration on save', async function (assert) {
      assert.expect(2);
      const pipelineID = this.pipeline.id;
      this.server.post(
        '/connectors',
        function ({ connectors }, { requestBody }) {
          let attrs = JSON.parse(requestBody);

          assert.deepEqual(
            attrs,
            {
              config: {
                name: 'My Kafka Connector',
                settings: {
                  servers: 'sleep',
                  topic: 'token',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'pkg/plugins/kafka/kafka',
              type: 'TYPE_DESTINATION',
            },
            'it calls the API with the correct protocol'
          );
          return connectors.create(attrs);
        }
      );
      await fillIn(page.connectorModalNameInput, 'My Kafka Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.destinationOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], 'token');

      await click('[data-test-connector-modal-create-button]');

      assert.dom(page.connectorModalNameInput).doesNotExist();
    });
  });
});
