import Route from '@ember/routing/route';

export default class PipelinesIndexRoute extends Route {
  model() {
    return this.store.findAll('pipeline', { reload: true });
  }
}
