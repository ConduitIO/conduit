import { module, test } from 'qunit';
import { visit, click, fillIn } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorModalNameInput: '[data-test-connector-modal-input="name"]',
  connectorModalPluginSelect: {
    select: '[data-test-connector-modal-select="connector-plugin"]',
    sourceOption: '[data-test-select-option-button="S3 Source"]',
    destinationOption: '[data-test-select-option-button="S3 Destination"]',
  },

  connectorModalConfigFields: '[data-test-config-field]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',

  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',
  pipelineAddNewDestinationButton:
    '[data-test-pipeline-editor-add-node-destination]',

  connectorModalOptionalTab: '[data-test-connector-modal-optional-tab]',
};

module('Acceptance | pipeline/index/connectors/s3', function (hooks) {
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
      assert.dom('[data-test-config-field="aws_access-key-id"]').exists();
      assert
        .dom('[data-test-config-field="aws_access-key-id"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="aws_bucket"]').exists();
      assert
        .dom('[data-test-config-field="aws_bucket"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="aws_region"]').exists();
      assert
        .dom('[data-test-config-field="aws_region"]')
        .hasAttribute('type', 'button');

      assert.dom('[data-test-config-field="aws_secret-access-key"]').exists();
      assert
        .dom('[data-test-config-field="aws_secret-access-key"]')
        .hasAttribute('type', 'text');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);
      assert.dom('[data-test-config-field="polling-period"]').exists();
      assert
        .dom('[data-test-config-field="polling-period"]')
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
                name: 'My S3 Connector',
                settings: {
                  'aws.access-key-id': 'sleep',
                  'aws.bucket': 'jaws',
                  'aws.region': 'us-east-1',
                  'aws.secret-access-key': 'token',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'builtin:s3',
              type: 'TYPE_SOURCE',
            },
            'it calls the API with the correct protocol'
          );
          return connectors.create(attrs);
        }
      );
      await fillIn(page.connectorModalNameInput, 'My S3 Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.sourceOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], 'token');
      await fillIn(configFields[3], 'jaws');

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
      assert.dom('[data-test-config-field="aws_access-key-id"]').exists();
      assert
        .dom('[data-test-config-field="aws_access-key-id"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="aws_bucket"]').exists();
      assert
        .dom('[data-test-config-field="aws_bucket"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="aws_region"]').exists();
      assert
        .dom('[data-test-config-field="aws_region"]')
        .hasAttribute('type', 'button');

      assert.dom('[data-test-config-field="aws_secret-access-key"]').exists();
      assert
        .dom('[data-test-config-field="aws_secret-access-key"]')
        .hasAttribute('type', 'text');

      assert.dom('[data-test-config-field="format"]').exists();
      assert
        .dom('[data-test-config-field="format"]')
        .hasAttribute('type', 'button');
    });

    test('shows the connectors optional fields', async function (assert) {
      await click(page.connectorModalOptionalTab);
      assert.dom('[data-test-config-field="buffer-size"]').exists();
      assert
        .dom('[data-test-config-field="buffer-size"]')
        .hasAttribute('type', 'number');

      assert.dom('[data-test-config-field="prefix"]').exists();
      assert
        .dom('[data-test-config-field="prefix"]')
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
                name: 'My S3 Connector',
                settings: {
                  'aws.access-key-id': 'sleep',
                  'aws.bucket': 'jaws',
                  'aws.region': 'us-east-1',
                  'aws.secret-access-key': 'token',
                  format: 'json',
                },
              },
              pipeline_id: pipelineID,
              plugin: 'builtin:s3',
              type: 'TYPE_DESTINATION',
            },
            'it calls the API with the correct protocol'
          );
          return connectors.create(attrs);
        }
      );
      await fillIn(page.connectorModalNameInput, 'My S3 Connector');
      await click(page.connectorModalPluginSelect.select);
      await click(page.connectorModalPluginSelect.destinationOption);

      const configFields = document.querySelectorAll(
        page.connectorModalConfigFields
      );

      await fillIn(configFields[0], 'sleep');
      await fillIn(configFields[1], 'token');
      await fillIn(configFields[3], 'jaws');

      await click('[data-test-connector-modal-create-button]');

      assert.dom(page.connectorModalNameInput).doesNotExist();
    });
  });
});
