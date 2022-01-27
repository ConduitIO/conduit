import { assert, module, test } from 'qunit';
import { find, visit, click, waitUntil } from '@ember/test-helpers';
import { setupApplicationTest } from 'ember-qunit';
import { setupMirage } from 'ember-cli-mirage/test-support';
import { Response } from 'ember-cli-mirage';

const page = {
  pipelineSubheaderName: '[data-test-pipeline-subheader-name]',
  pipelineSubheaderDescription: '[data-test-pipeline-subheader-description]',
  pipelineTopNavIndexLink: '[data-test-pipeline-top-nav="pipelines-link"]',
  pipelineTopNavLink: '[data-test-pipeline-top-nav="pipeline-link"]',

  pipelineEditorZeroState: '[data-test-pipeline-zero-state]',
  pipelineEditorZeroStateSourceButton:
    '[data-test-pipeline-zero-state-button="source"]',

  pipelineEditorStreamNode: '[data-test-stream-node]',
  pipelineEditorSourceNodes:
    '[data-test-connector-column="source"] [data-test-connector-node]',
  pipelineEditorDestinationNodes:
    '[data-test-connector-column="destination"] [data-test-connector-node]',

  pipelineAddNewNodeButton: '[data-test-pipeline-editor-add-node]',
  pipelineAddNewSourceButton: '[data-test-pipeline-editor-add-node-source]',

  pipelineStatus: '[data-test-pipeline-status-label]',
  pipelineStatusIndicator: '[data-test-pipeline-status-indicator]',
  pipelineStatusButton: '[data-test-pipeline-status] button',
  pipelineStatusStart: "[data-test-pipeline-status-action='start']",
  pipelineStatusStop: "[data-test-pipeline-status-action='stop']",

  connectorOverviewListItem: '[data-test-connector-overview-list-item]',
  connectorOverviewButton: '[data-test-connector-overview-button]',

  newConnectorModal: '[data-test-connector-modal="new"]',
  newConnectorModalCancelButton: '[data-test-connector-modal-cancel-button]',

  errorTitle: '[data-test-error-title]',
  errorDismiss: '[data-test-error-dismiss]',

  pipelineDegradedButton: '[data-test-pipeline-degraded-button]',
  pipelineErrorModal: '[data-test-pipeline-error-modal]',
};

module('Acceptance | pipeline/index', function (hooks) {
  setupApplicationTest(hooks);
  setupMirage(hooks);

  module('viewing a pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', {
        config: {
          name: 'the eldian to titan pipeline',
          description: 'this pipeline takes eldians and turns them into titans',
          connectorConfigs: [],
        },
      });
      this.set('pipeline', pipeline);

      await visit(`/pipelines/${pipeline.id}`);
    });

    test('it shows pipeline name in the sub header', function (assert) {
      assert
        .dom(page.pipelineSubheaderName)
        .containsText('the eldian to titan pipeline');
    });

    test('it shows pipeline description in the sub header', function (assert) {
      assert
        .dom(page.pipelineSubheaderDescription)
        .containsText('this pipeline takes eldians and turns them into titans');
    });

    test('it links to the list of pipelines in the header', function (assert) {
      assert.dom(page.pipelineTopNavIndexLink).containsText('Pipelines');
      assert
        .dom(page.pipelineTopNavIndexLink)
        .hasAttribute('href', '/ui/pipelines');
    });

    test('it links to the current pipeline in the header', function (assert) {
      assert
        .dom(page.pipelineTopNavLink)
        .containsText('the eldian to titan pipeline');
      assert
        .dom(page.pipelineTopNavLink)
        .hasAttribute('href', `/ui/pipelines/${this.pipeline.id}`);
    });
  });

  module('viewing a pipeline with no connectors', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');
      await visit(`/pipelines/${pipeline.id}`);
    });

    test('it shows the pipeline zero state', function (assert) {
      assert.dom(page.pipelineEditorZeroState).exists();
    });
  });

  module('viewing an existing pipeline with connectors', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withGenericConnectors');
      await visit(`/pipelines/${pipeline.id}`);
    });

    test('it shows the connectors in the pipeline', function (assert) {
      assert.dom(page.pipelineEditorStreamNode).exists();
      assert.dom(page.pipelineEditorSourceNodes).exists({ count: 1 });
      assert.dom(page.pipelineEditorDestinationNodes).exists({ count: 2 });

      assert.dom(page.connectorOverviewListItem).exists({ count: 3 });
    });

    test('it displays the pipeline status', function (assert) {
      assert.dom(page.pipelineStatus).containsText('paused');
    });
  });

  module('adding a connector to a pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');

      await visit(`/pipelines/${pipeline.id}`);
    });

    test('it shows the connector modal via clicking the zero state', async function (assert) {
      await click(page.pipelineEditorZeroStateSourceButton);
      assert.dom(page.newConnectorModal).exists();
    });

    test('it shows the connector modal via clicking through the connector overview panel', async function (assert) {
      await click(page.pipelineAddNewNodeButton);
      await click(page.pipelineAddNewSourceButton);
      assert.dom(page.newConnectorModal).exists();
    });

    test('it can be canceled', async function (assert) {
      await click(page.pipelineAddNewNodeButton);
      await click(page.pipelineAddNewSourceButton);
      await click(page.newConnectorModalCancelButton);
      assert.dom(page.newConnectorModal).doesNotExist();
    });
  });

  module('updating the pipeline status', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withGenericConnectors');

      await visit(`/pipelines/${pipeline.id}`);
      this.server.get('/pipelines/:id', function ({ pipelines }, request) {
        const id = request.params.id;
        assert.ok(true);
        return pipelines.find(id);
      });
      await click(page.pipelineStatusButton);
      await click(page.pipelineStatusStart);
    });

    test('it updates successfully and polls the running pipeline', async function (assert) {
      assert.dom(page.pipelineStatus).containsText('running');

      // We reload the pipeline up front, and then again when polling
      // 3 assertions total (including dom assertion) to confirm polling works.
      assert.expect(3);
    });
  });

  module('starting a pipeline that synchronously errors out', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline');
      this.set('pipeline', pipeline);

      this.server.post('/pipelines/:id/start', function () {
        return new Response(
          500,
          {},
          {
            code: 13,
            message: 'failed to start pipeline',
            details: [],
          }
        );
      });

      await visit(`/pipelines/${pipeline.id}`);

      await click(page.pipelineStatusButton);

      await click(page.pipelineStatusStart);
    });

    test('it displays an error notification', function (assert) {
      assert.dom(page.errorTitle).containsText('failed to start pipeline');
    });
  });

  module('stopping a pipeline that synchronously errors out', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create(
        'pipeline',
        { state: { status: 'STATUS_RUNNING' } },
        'withFileConnectors'
      );
      this.set('pipeline', pipeline);

      this.server.post('/pipelines/:id/stop', function () {
        return new Response(
          500,
          {},
          {
            code: 13,
            message: 'failed to stop pipeline',
            details: [],
          }
        );
      });

      await visit(`/pipelines/${pipeline.id}`);

      await click(page.pipelineStatusButton);

      await click(page.pipelineStatusStop);
    });

    test('it displays an error notification', function (assert) {
      assert.dom(page.errorTitle).containsText('failed to stop pipeline');
    });
  });

  module(
    'starting a pipeline that asynchronously errors out',
    function (hooks) {
      hooks.beforeEach(async function () {
        const pipeline = this.server.create('pipeline', 'withFileConnectors');
        this.set('pipeline', pipeline);

        await visit(`/pipelines/${pipeline.id}`);

        await click(page.pipelineStatusButton);

        // Don't wait for the click to resolve
        click(page.pipelineStatusStart);

        // Instead wait only for the upfront pipeline reload
        await waitUntil(
          function () {
            return find(page.pipelineStatus).textContent.includes('running');
          },
          { timeout: 2000 }
        );

        // Set errored status on poll tick
        this.server.get('/pipelines/:id', function ({ pipelines }, request) {
          const id = request.params.id;
          const pipeline = pipelines.find(id);
          pipeline.update('state', {
            status: 'STATUS_DEGRADED',
            error: 'beepboop',
          });
          return pipeline;
        });
        await waitUntil(
          function () {
            return find(page.pipelineStatus).textContent.includes('paused');
          },
          { timeout: 2000 }
        );
      });

      test('it displays the degraded status', function (assert) {
        assert.dom(page.pipelineStatus).containsText('paused');
        assert.dom(page.pipelineStatusIndicator).hasClass('bg-orange-700');
      });

      test('it displays an error notification', function (assert) {
        assert
          .dom(page.errorTitle)
          .containsText(
            `error while running the pipeline ${this.pipeline.config.name}`
          );
      });

      test('it displays the full error when dismissing the notification', async function () {
        await click(page.errorDismiss);
        assert.dom(page.pipelineErrorModal).containsText('beepboop');
      });
    }
  );

  module('viewing a degraded pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create(
        'pipeline',
        'degraded',
        'withFileConnectors'
      );
      await visit(`/pipelines/${pipeline.id}`);
    });

    test('it shows the degraded button to view the full error', async function () {
      await click(page.pipelineDegradedButton);
      assert.dom(page.pipelineErrorModal).containsText('beepboop');
    });
  });

  module('viewing a healthy running pipeline', function (hooks) {
    hooks.beforeEach(async function () {
      const pipeline = this.server.create('pipeline', 'withFileConnectors');
      await visit(`/pipelines/${pipeline.id}`);
    });

    test('does not show a degraded button', async function () {
      assert.dom(page.pipelineDegradedButton).doesNotExist();
    });
  });
});
