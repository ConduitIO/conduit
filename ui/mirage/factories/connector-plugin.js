import { Factory, trait } from 'ember-cli-mirage';
import { generateBlueprint } from 'conduit-ui/utils/blueprints/generate-blueprint-data';

export default Factory.extend({
  name: 'Some Connector Plugin',
  connectorType: 'source',
  pluginPath: 'pkg/plugins/foo/bar',

  source: trait({
    connectorType: 'source',
  }),

  destination: trait({
    connectorType: 'destination',
  }),

  // Bare minimum blueprint
  blueprint() {
    const requiredString = generateBlueprint(
      'path',
      'File Path',
      'Enter path to file',
      'string',
      { isRequired: true }
    );

    return [requiredString];
  },

  // Ultra generic blueprint with multiple input types
  withGenericBlueprint: trait({
    blueprint() {
      const requiredText = generateBlueprint(
        'titan:name',
        'Titan Name',
        'Enter Titan Name',
        'string',
        { isRequired: true }
      );
      const requiredNumber = generateBlueprint(
        'titan:height',
        'Titan Height',
        'Enter Titan Height',
        'int',
        { isRequired: true }
      );
      const requiredSelect = generateBlueprint(
        'titan:type',
        'Titan Type',
        'Enter Titan Type',
        'string',
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
        'boolean'
      );

      return [
        requiredText,
        requiredNumber,
        requiredSelect,
        optionalText,
        optionalBoo,
      ];
    },
  }),
});
