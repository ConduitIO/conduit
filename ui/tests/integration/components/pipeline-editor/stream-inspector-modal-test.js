import { module, test } from 'qunit';
import { setupRenderingTest } from 'ember-qunit';
import { click, render } from '@ember/test-helpers';
import { hbs } from 'ember-cli-htmlbars';
import {
  mockSocket,
  mockEntityInspection,
} from 'conduit-ui/tests/helpers/websockets';

module(
  'Integration | Component | pipeline-editor/stream-inspector',
  function (hooks) {
    setupRenderingTest(hooks);

    hooks.beforeEach(function () {
      this.socket = mockSocket();
      const service = this.owner.lookup('service:websockets');
      service._entities.set('123', mockEntityInspection('123', this.socket));
      this.dummyDismiss = () => {};
      this.pipeline = { isRunning: true };
    });

    module('with no websocket data', function () {
      test('it waits for data by default', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );

        assert.dom('[data-test-stream-inspector="empty-record"]').exists();
      });

      test('it shows an info message when inspecting a single record', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );

        await click('[data-test-stream-inspector-show-single-record]');
        assert
          .dom('[data-test-stream-inspector-single-record]')
          .includesText('Waiting for data');
      });
    });

    module('when websocket data is triggered', function () {
      test('it displays the last record', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );
        this.socket.triggerRecord();

        assert.dom('[data-test-stream-inspector="json-record"]').exists();
        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .includesText('position');
        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .includesText('operation');
        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .includesText('metadata');
        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .includesText('key');
        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .includesText('payload');
      });

      test('it displays at max the last 10 records', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );
        this.socket.triggerRecords(20);

        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .exists({ count: 10 });
      });

      test('it shows the last record when inspecting a single record', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );

        this.socket.triggerRecords(10);

        await click('[data-test-stream-inspector-show-single-record]');
        assert
          .dom('[data-test-stream-inspector-single-record]')
          .includesText(
            '{ position: "", operation: "", metadata: {}, key: {}, payload: {} }'
          );
      });
    });

    module('pausing the inspector', function () {
      test('it updates the button', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );
        this.socket.triggerRecord();

        await click('[data-test-stream-inspector-button="pause"]');
        assert.dom('[data-test-stream-inspector-button="resume"]').exists();
      });

      test('it stops tracking records', async function (assert) {
        await render(
          hbs`<PipelineEditor::StreamInspectorModal
                @onDismiss={{this.dummyDismiss}}
                @pipeline={{this.pipeline}}
                @entityId="123"/>`
        );

        this.socket.triggerRecords(3);

        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .exists({ count: 3 });

        await click('[data-test-stream-inspector-button="pause"]');

        this.socket.triggerRecords(5);

        assert
          .dom('[data-test-stream-inspector="json-record"]')
          .exists({ count: 3 });
      });
    });
  }
);
