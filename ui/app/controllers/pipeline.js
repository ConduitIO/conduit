import Controller, { inject as controller } from '@ember/controller';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';
import { waitFor } from '@ember/test-waiters';

export default class PipelineController extends Controller {
  @service
  flashMessages;

  @controller('pipelines')
  pipelinesController;

  @action
  @waitFor
  async startPipeline(pipeline) {
    try {
      await pipeline.startPipeline();
    } catch (error) {
      this._handleStartStopPipelineError(error);
      return;
    }

    await pipeline.reload();
    pipeline.onPipelineEvent(
      'onPipelineDegraded',
      this,
      this._handlePipelineRunError
    );
    pipeline.pollPipeline.perform();
  }

  @action
  @waitFor
  async stopPipeline(pipeline) {
    try {
      await pipeline.stopPipeline();
    } catch (error) {
      this._handleStartStopPipelineError(error);
      return;
    }
    await pipeline.reload();
  }

  _handlePipelineRunError(pipeline) {
    this.flashMessages.add({
      type: 'error',
      message: `There was an error while running the pipeline ${pipeline.name}`,
      sticky: true,
      destroyOnClick: false,
      customButtonText: 'View full error',
      onDestroy: () => {
        this.pipelinesController.setPipelineRunningError(pipeline.state.error);
      },
    });
  }

  _handleStartStopPipelineError(error) {
    if (error.response?.data) {
      const message = error.response.data.message;
      this.flashMessages.add({
        type: 'error',
        message,
        sticky: true,
      });
    } else {
      this.flashMessages.add({
        type: 'error',
        message: error.message,
        sticky: true,
      });
    }
  }
}
