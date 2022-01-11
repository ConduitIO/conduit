import Route from '@ember/routing/route';

export default class TransformsRoute extends Route {
  model() {
    return this.store.findAll('transform');
  }
}
