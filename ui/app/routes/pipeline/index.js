import Route from '@ember/routing/route';
import Transforms from 'conduit-ui/utils/transforms/transforms';

export default class PipelineIndexRoute extends Route {
  async model() {
    let connectorPlugins, transforms;

    const pipeline = this.modelFor('pipeline').pipeline;

    let allPipelines;
    if (pipeline.isNew) {
      allPipelines = await this.store.findAll('pipeline', { reload: true });
    } else {
      allPipelines = null;

      connectorPlugins = await this.store.findAll('plugin');

      this.store.pushPayload('transform', Transforms);
      transforms = this.store.peekAll('transform');
    }

    return {
      connectorPlugins,
      transforms,
      pipeline,
      allPipelines,
    };
  }

  activate() {
    const pipeline = this.modelFor('pipeline').pipeline;
    if (!pipeline.isNew) {
      pipeline.pollPipeline.perform();
    }
  }

  deactivate() {
    const pipeline = this.modelFor('pipeline').pipeline;
    if (!pipeline.isNew) {
      pipeline.pollPipeline.cancelAll();
    }
  }
}
