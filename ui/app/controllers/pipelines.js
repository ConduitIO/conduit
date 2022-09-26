import Controller from '@ember/controller';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';

export default class PipelinesController extends Controller {
  @service
  router;

  @tracked
  confirmDeletePipeline = null;

  @tracked
  pipelineRunningError = null;

  get isLongPipelineName() {
    return this.confirmDeletePipeline?.name.length > 32;
  }

  @action
  setConfirmDeletePipeline(value) {
    this.confirmDeletePipeline = value;
  }

  @action
  setPipelineRunningError(value) {
    this.pipelineRunningError = value;
  }

  @action
  async destroyPipeline(pipeline) {
    try {
      await pipeline.destroyRecord();
    } catch (e) {
      pipeline.rollbackAttributes();
    }
    this.setConfirmDeletePipeline(null);
    this.router.transitionTo('pipelines');
  }
}
