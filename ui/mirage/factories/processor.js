import { Factory } from 'miragejs';

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
