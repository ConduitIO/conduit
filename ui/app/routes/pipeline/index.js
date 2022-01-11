import Route from '@ember/routing/route';
import Transforms from 'conduit-ui/utils/transforms/transforms';
import ConnectorPlugins from 'conduit-ui/utils/connector-plugins/connector-plugins';

export default class PipelineIndexRoute extends Route {
  async model() {
    let connectorPlugins, transforms;

    const pipeline = this.modelFor('pipeline').pipeline;

    let allPipelines;
    if (pipeline.isNew) {
      allPipelines = await this.store.findAll('pipeline', { reload: true });
    } else {
      allPipelines = null;

      this.store.pushPayload('connector-plugin', ConnectorPlugins);
      connectorPlugins = this.store.peekAll('connector-plugin');

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
