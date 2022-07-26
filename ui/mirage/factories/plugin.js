import { Factory, trait } from 'ember-cli-mirage';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default Factory.extend({
  id: 'builtin:file',
  name: 'builtin:file',

  source: trait({
    sourceParams() {
      return this.blueprint;
    },
  }),

  destination: trait({
    destinationParams() {
      return this.blueprint;
    },
  }),

  sourceAndDestination: trait({
    sourceParams() {
      return this.blueprint;
    },
    destinationParams() {
      return this.blueprint;
    },
  }),

  sourceParams() {
    return {};
  },

  destinationParams() {
    return {};
  },

  // Bare minimum blueprint
  blueprint() {
    const requiredString = generateBlueprint(
      'path',
      'path',
      'File path',
      'TYPE_STRING',
      { isRequired: true }
    );

    return { ...requiredString };
  },

  // Ultra generic blueprint with multiple input types
  withGenericBlueprint: trait({
    id: 'builtin:generic',
    name: 'builtin:generic',
    blueprint() {
      const requiredText = generateBlueprint(
        'titanName',
        'Titan Name',
        'Enter Titan Name',
        'TYPE_STRING',
        { isRequired: true }
      );
      const requiredNumber = generateBlueprint(
        'titan:height',
        'Titan Height',
        'Enter Titan Height',
        'TYPE_NUMBER',
        { isRequired: true }
      );
      const requiredSelect = generateBlueprint(
        'titan.type',
        'Titan Type',
        'Enter Titan Type',
        'TYPE_STRING',
        { isRequired: true }
      );

      const optionalText = generateBlueprint(
        'titan:description',
        'Titan Description',
        'Enter Titan Description'
      );
      const optionalBoo = generateBlueprint(
        'titan:founding',
        'Founding',
        'Founding',
        'TYPE_BOOL'
      );

      return {
        ...requiredText,
        ...requiredNumber,
        ...requiredSelect,
        ...optionalText,
        ...optionalBoo,
      };
    },
  }),
});
