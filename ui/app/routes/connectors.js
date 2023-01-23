import Route from '@ember/routing/route';
import { inject as service } from '@ember/service';

export default class ConnectorsRoute extends Route {
  @service
  store;

  async model() {
    return this.store.findAll('plugin');
  }
}
