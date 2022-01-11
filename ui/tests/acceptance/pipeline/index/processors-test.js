import { assert, module, test } from 'qunit';
import { visit, click, fillIn, findAll, find } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';

const page = {
  connectorSlidePanel: '[data-test-connector-slide-panel]',
  transformsTab: '[data-test-transforms-tab]',
  addTransformButton: '[data-test-button="add-connector-transform"]',
  saveTransformButton: '[data-test-button="save-connector-transform"]',
  updateTransformButton: '[data-test-button="update-connector-transform"]',
  availableTransformMaskField: '[data-test-available-transform="mask-field"]',
  transformOptionsTrigger:
    '[data-test-dropdown-trigger="connector-transform-options"]',
  deleteTransformButton:
    '[data-test-dropdown-button="delete-connector-transform"]',
  connectorTransforms: '[data-test-connector-transform]',
  configFields: '[data-test-config-field]',
  sourceNode: '[data-test-connector-node="source-source-one"] > div',
  destinationNode: '[data-test-connector-node="destination-destination-one"] > div',
};

module('Acceptance | pipeline/index/processors', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  module('adding a processor to a connector', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withGenericConnectors');
      await visit(`/pipelines/${pipeline.id}`);
      await click(page.sourceNode);
      await click(page.transformsTab);
      await click(page.addTransformButton);
      await click(page.availableTransformMaskField);
      await fillIn(findAll(page.configFields)[0], 'maskme');
      await fillIn(findAll(page.configFields)[1], '~*~*~*~*~');
      await click(page.saveTransformButton);
    });

    test('creates the processor attached to the connector', function () {
      assert.dom(page.connectorTransforms).exists({ count: 1 });
    });

    test('it retains all the processor values', async function () {
      await click(
        find(`${page.connectorTransforms} [data-test-button="edit-transform"]`)
      );
      assert.dom(findAll(page.configFields)[0]).hasValue('maskme');
      assert.dom(findAll(page.configFields)[1]).hasValue('~*~*~*~*~');
    });
  });

  module('editing a processor', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withGenericConnectors');
      const connector = this.server.schema.find(
        'connector',
        pipeline.connectorIds.firstObject
      );
      this.server.create('processor', {
        parent: {
          type: 'TYPE_CONNECTOR',
          id: connector.id,
        },
      });
      await visit(`/pipelines/${pipeline.id}`);
      await click(page.sourceNode);
      await click(page.transformsTab);
      await click(
        find(`${page.connectorTransforms} [data-test-button="edit-transform"]`)
      );
      await fillIn(findAll(page.configFields)[0], 'maskmenext');
      await fillIn(findAll(page.configFields)[1], '<><><>');
      await click(page.updateTransformButton);
    });

    test('it updates with the edited values', async function (assert) {
      await click(
        find(`${page.connectorTransforms} [data-test-button="edit-transform"]`)
      );
      assert.dom(findAll(page.configFields)[0]).hasValue('maskmenext');
      assert.dom(findAll(page.configFields)[1]).hasValue('<><><>');
    });
  });

  module('deleting a processor', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withGenericConnectors');
      const connector = this.server.schema.find(
        'connector',
        pipeline.connectorIds.firstObject
      );
      this.server.create('processor', {
        parent: {
          type: 'TYPE_CONNECTOR',
          id: connector.id,
        },
      });
      await visit(`/pipelines/${pipeline.id}`);
      await click(page.sourceNode);
      await click(page.transformsTab);
      await click(
        find(`${page.connectorTransforms} [data-test-button="edit-transform"]`)
      );
      await click(page.transformOptionsTrigger);
      await click(page.deleteTransformButton);
    });

    test('it updates with the edited values', function (assert) {
      assert.dom(page.connectorTransforms).doesNotExist();
    });
  });
});
