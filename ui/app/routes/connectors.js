import Route from '@ember/routing/route';

export default class ConnectorsRoute extends Route {
  async model() {
    return this.store.findAll('connector-plugin');
  }
}
