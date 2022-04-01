'use strict';

module.exports = function (environment) {
  const conduitAPIURL =
    environment === 'production'
      ? null
      : process.env.CONDUIT_API_URL || 'http://localhost:8080';
  let ENV = {
    modulePrefix: 'conduit-ui',
    environment,
    rootURL: '/ui/',
    locationType: 'auto',
    conduitAPIURL,
    isDevMirageEnabled: process.env.ENABLE_DEV_MIRAGE === 'true' || false,
    EmberENV: {
      FEATURES: {
        // Here you can enable experimental features on an ember canary build
        // e.g. EMBER_NATIVE_DECORATOR_SUPPORT: true
      },
      EXTEND_PROTOTYPES: {
        // Prevent Ember Data from overriding Date.parse.
        Date: false,
      },
    },

    APP: {
      // Here you can pass flags/options to your application instance
      // when it is created
    },
  };

  if (environment === 'development') {
    ENV['ember-cli-mirage'] = {
      enabled: ENV.isDevMirageEnabled,
    };

    // ENV.APP.LOG_RESOLVER = true;
    // ENV.APP.LOG_ACTIVE_GENERATION = true;
    // ENV.APP.LOG_TRANSITIONS = true;
    // ENV.APP.LOG_TRANSITIONS_INTERNAL = true;
    // ENV.APP.LOG_VIEW_LOOKUPS = true;
  }

  if (environment === 'test') {
    // Testem prefers this...
    ENV.locationType = 'none';

    // keep test console output quieter
    ENV.APP.LOG_ACTIVE_GENERATION = false;
    ENV.APP.LOG_VIEW_LOOKUPS = false;

    ENV.APP.rootElement = '#ember-testing';
    ENV.APP.autoboot = false;
  }

  if (environment === 'production') {
    if (process.env.DEPLOY_TARGET === 'staging') {
      ENV['ember-cli-mirage'] = {
        enabled: true,
      };

      ENV.rootURL = '/';
    }
  }

  return ENV;
};
