self.deprecationWorkflow = self.deprecationWorkflow || {};

self.deprecationWorkflow.config = {
  workflow: [
    { handler: 'silence', matchId: 'manager-capabilities.modifiers-3-13' },
    { handler: 'silence', matchId: 'this-property-fallback' },
    {
      handler: 'silence',
      matchId: 'ember.built-in-components.legacy-arguments',
    },
    { handler: 'silence', matchId: 'routing.transition-methods' },
  ],
};
