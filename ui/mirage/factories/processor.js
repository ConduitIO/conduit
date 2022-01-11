import { Factory } from 'ember-cli-mirage';

export default Factory.extend({
  name: 'maskfieldkey',

  config() {
    return {
      settings: {
        field: 'maskme',
        replacement: '~*~*~*~*',
      },
    };
  },

  type: 'TYPE_TRANSFORM',

  parent() {
    return {};
  },
});
