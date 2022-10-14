import { Factory } from 'ember-cli-mirage';

export default Factory.extend({
  type: 'maskfieldkey',

  config() {
    return {
      settings: {
        field: 'maskme',
        replacement: '~*~*~*~*',
      },
    };
  },

  parent() {
    return {};
  },
});
