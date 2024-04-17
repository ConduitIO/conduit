import { Factory, trait } from 'miragejs';

export default Factory.extend({
  config() {
    return { name: 'Some Source Connector', settings: {} };
  },

  state() {
    return { position: '' };
  },

  type: 'TYPE_SOURCE',

  withPopulatedGenericConfig: trait({
    afterCreate(connector) {
      const config = {
        name: connector.config.name,
        settings: {
          titanName: 'eren jaeger',
          'titan:height': 50,
          'titan.type': 'attack',
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
