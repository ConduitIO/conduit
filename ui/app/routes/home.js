import Route from '@ember/routing/route';

export default class HomeRoute extends Route {
  redirect() {
    this.transitionTo('pipelines');
  }
}
