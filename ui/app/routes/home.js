import { inject as service } from '@ember/service';
import Route from '@ember/routing/route';

export default class HomeRoute extends Route {
  @service('router') router;
  redirect() {
    this.router.transitionTo('pipelines');
  }
}
