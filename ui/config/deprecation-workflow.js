/* eslint-disable no-undef */
self.deprecationWorkflow = self.deprecationWorkflow || {};

self.deprecationWorkflow.config = {
  workflow: [
    { handler: 'silence', matchId: 'ember-global' },
    { handler: 'silence', matchId: 'ember.component.reopen' },
    { handler: 'silence', matchId: 'ember-keyboard.first-responder-inputs' },
    { handler: 'silence', matchId: 'implicit-injections' },
    { handler: 'silence', matchId: 'ember-test-waiters-legacy-module-name' },
    { handler: 'silence', matchId: 'ember-keyboard.old-propagation-model' },
    {
      handler: 'silence',
      matchId: 'deprecated-run-loop-and-computed-dot-access',
    },
    {
      handler: 'silence',
      matchId: 'ember.built-in-components.legacy-arguments',
    },
    { handler: 'silence', matchId: 'routing.transition-methods' },
  ],
};
