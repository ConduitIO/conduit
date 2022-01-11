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
    await pipeline.startPipeline();
    await pipeline.reload();
    pipeline.on('onPipelineDegraded', this, this._handlePipelineRunError);
    pipeline.pollPipeline.perform();
    return;
  }

  @action
  async stopPipeline(pipeline) {
    await pipeline.stopPipeline();
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
}
