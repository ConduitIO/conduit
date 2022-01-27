import EmberRouter from '@ember/routing/router';
import config from 'conduit-ui/config/environment';

export default class Router extends EmberRouter {
  location = config.locationType;
  rootURL = config.rootURL;
}

Router.map(function () {
  this.route('home', { path: '/' });
  this.route('pipelines', function () {
    // => this.route('index');

    this.route(
      'pipeline',
      { path: '/:pipeline_id', resetNamespace: true },
      function () {
        // => this.route('index');
        this.route('settings');
      }
    );
  });

  this.route('connectors');
  this.route('transforms');
  this.route('settings');
});
