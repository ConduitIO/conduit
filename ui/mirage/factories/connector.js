import { Factory, trait } from 'ember-cli-mirage';

export default Factory.extend({
  config() {
    return { name: 'Some Source Connector', settings: {} };
  },

  plugin: 'pkg/plugins/file/file',

  state() {
    return { position: '' };
  },

  type: 'TYPE_SOURCE',

  withPopulatedGenericConfig: trait({
    afterCreate(connector) {
      const config = {
        name: connector.config.name,
        settings: {
          'titan:name': 'eren jaeger',
          'titan:height': 50,
          'titan:type': 'attack',
        },
      };

      connector.update({
        config,
      });
    },
  }),

  withPopulatedFileConfig: trait({
    afterCreate(connector) {
      const config = {
        name: connector.config.name,
        settings: {
          path: 'path/to/file.txt',
        },
      };

      connector.update({
        config,
      });
    },
  }),
});
