import config from 'conduit-ui/config/environment';
import { createServer } from 'miragejs';

import { discoverEmberDataModels } from 'ember-cli-mirage';

export default function (cfg) {
  let finalConfig = {
    ...cfg,
    models: { ...discoverEmberDataModels(cfg.store), ...cfg.models },
    routes,
  };
  return createServer(finalConfig);
}

function routes() {
  // v1 Conduit REST API
  this.urlPrefix = config.conduitAPIURL;
  this.namespace = 'v1';

  if (config.isDevMirageEnabled || config.environment === 'test') {
    this.get('/pipelines');
    this.get('/pipelines/:id');
    this.post('/pipelines', function ({ pipelines }, request) {
      let attrs = JSON.parse(request.requestBody);
      attrs.state = {
        status: 'STATUS_STOPPED',
        error: '',
      };

      return pipelines.create(attrs);
    });
    this.put('/pipelines/:id');
    this.delete('/pipelines/:id');

    this.post('/pipelines/:id/start', function ({ pipelines }, request) {
      const pipeline = pipelines.find(request.params.id);
      pipeline.update('state', { status: 'STATUS_RUNNING', error: '' });

      return {};
    });

    this.post('/pipelines/:id/stop', function ({ pipelines }, request) {
      const pipeline = pipelines.find(request.params.id);
      pipeline.update('state', { status: 'STATUS_STOPPED', error: '' });

      return {};
    });

    this.get('/connectors', function ({ connectors }, request) {
      const conns = connectors.all();
      const pipelineConns = conns.models.filter((connector) => {
        return connector.pipelineId === request.queryParams.pipeline_id;
      });

      return pipelineConns;
    });

    this.post(
      '/connectors',
      function ({ connectors, plugins, pipelines }, request) {
        const attrs = JSON.parse(request.requestBody);
        const plugin = plugins.find(attrs.plugin);
        const pipeline = pipelines.find(attrs.pipeline_id);

        return connectors.create({
          type: attrs.type,
          config: attrs.config,
          pipeline,
          plugin,
        });
      },
    );

    this.put('/connectors/:id');
    this.delete('/connectors/:id');

    this.get('/processors');
    this.post('/processors');
    this.put('/processors/:id');
    this.delete('/processors/:id');

    this.get('/plugins');
  } else {
    this.passthrough('/pipelines');
    this.passthrough('/pipelines/:id');
    this.passthrough('/pipelines/:id/start');
    this.passthrough('/pipelines/:id/stop');
    this.passthrough('/connectors');
    this.passthrough('/connectors/:id');
    this.passthrough('/processors');
    this.passthrough('/processors/:id');
    this.passthrough('/plugins');
  }
}
