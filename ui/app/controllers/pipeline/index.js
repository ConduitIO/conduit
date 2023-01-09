import Controller from '@ember/controller';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';

export default class PipelineIndexController extends Controller {
  @service
  pipelineNodeManager;

  @service
  store;

  @service
  router;

  @action
  async createPipeline() {
    const pipeline = this.model.pipeline;
    try {
      await pipeline.save();
    } catch (error) {
      return;
    }
    this.router.replaceWith('pipeline', pipeline.id);
  }
}
