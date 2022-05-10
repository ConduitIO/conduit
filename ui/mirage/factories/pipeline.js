import { Factory, trait } from 'ember-cli-mirage';

export default Factory.extend({
  config(i) {
    return {
      name: `My Conduit Pipeline ${i + 1}`,
      description:
        'I am a pipeline description. Did you know? I am a pipeline description',
      connectorConfigs: [],
    };
  },

  state() {
    return { status: 'STATUS_STOPPED', error: '' };
  },

  degraded: trait({
    state() {
      return {
        status: 'STATUS_DEGRADED',
        error:
          'beepboop you havent declared sleep token as the best band in the world',
      };
    },
  }),

  connector_ids() {
    return [];
  },

  withFileConnectors: trait({
    afterCreate(pipeline, server) {
      let filePlugin = server.db.plugins.findBy({
        id: 'builtin:file',
      });

      if (!filePlugin) {
        filePlugin = server.create('plugin', 'source', 'destination');
      }

      server.create(
        'connector',
        {
          type: 'TYPE_SOURCE',
          config: { name: 'Source One' },
          pipeline,
          plugin: filePlugin,
        },
        'withPopulatedFileConfig'
      );

      server.create(
        'connector',
        {
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination One' },
          pipeline,
          plugin: filePlugin,
        },
        'withPopulatedFileConfig'
      );

      server.create(
        'connector',
        {
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination Two' },
          pipeline,
          plugin: filePlugin,
        },
        'withPopulatedFileConfig'
      );
    },
  }),

  withFilePlugins: trait({
    afterCreate(pipeline, server) {
      let filePlugin = server.db.plugins.findBy({
        id: 'builtin:file',
      });

      if (!filePlugin) {
        filePlugin = server.create('plugin', 'source', 'destination');
      }
    },
  }),

  withGenericConnectors: trait({
    afterCreate(pipeline, server) {
      let genericPlugin = server.db.plugins.findBy({
        name: 'builtin:generic',
      });

      if (!genericPlugin) {
        genericPlugin = server.create(
          'plugin',
          'source',
          'withGenericBlueprint'
        );
      }

      server.create(
        'connector',
        {
          plugin: genericPlugin,
          type: 'TYPE_SOURCE',
          config: { name: 'Source One' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );

      server.create(
        'connector',
        {
          plugin: genericPlugin,
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination One' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );

      server.create(
        'connector',
        {
          plugin: genericPlugin,
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination Two' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );
    },
  }),
});
