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
      let fileSourcePlugin = server.db.connectorPlugins.findBy({
        name: 'File Source',
      });

      let fileDestinationPlugin = server.db.connectorPlugins.findBy({
        name: 'File Destination',
      });

      if (!fileSourcePlugin) {
        fileSourcePlugin = server.create('connector-plugin', 'source', {
          name: 'File Source',
          pluginPath: 'builtin:file',
        });
      }

      if (!fileDestinationPlugin) {
        fileDestinationPlugin = server.create(
          'connector-plugin',
          'destination',
          {
            name: 'File Destination',
            pluginPath: 'builtin:file',
          }
        );
      }

      server.create(
        'connector',
        {
          type: 'TYPE_SOURCE',
          config: { name: 'Source One' },
          pipeline,
        },
        'withPopulatedFileConfig'
      );

      server.create(
        'connector',
        {
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination One' },
          pipeline,
        },
        'withPopulatedFileConfig'
      );

      server.create(
        'connector',
        {
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination Two' },
          pipeline,
        },
        'withPopulatedFileConfig'
      );
    },
  }),

  withGenericConnectors: trait({
    afterCreate(pipeline, server) {
      let genericSourcePlugin = server.db.connectorPlugins.findBy({
        name: 'Generic Source',
      });

      let genericDestinationPlugin = server.db.connectorPlugins.findBy({
        name: 'Generic Source',
      });

      if (!genericSourcePlugin) {
        genericSourcePlugin = server.create(
          'connector-plugin',
          'source',
          'withGenericBlueprint',
          {
            name: 'Generic Source',
          }
        );
      }

      if (!genericDestinationPlugin) {
        genericDestinationPlugin = server.create(
          'connector-plugin',
          'destination',
          'withGenericBlueprint',
          {
            name: 'Generic Destination',
          }
        );
      }

      server.create(
        'connector',
        {
          plugin: genericSourcePlugin.pluginPath,
          type: 'TYPE_SOURCE',
          config: { name: 'Source One' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );

      server.create(
        'connector',
        {
          plugin: genericSourcePlugin.pluginPath,
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination One' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );

      server.create(
        'connector',
        {
          plugin: genericSourcePlugin.pluginPath,
          type: 'TYPE_DESTINATION',
          config: { name: 'Destination Two' },
          pipeline,
        },
        'withPopulatedGenericConfig'
      );
    },
  }),
});
