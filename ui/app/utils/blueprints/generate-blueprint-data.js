import EmberObject from '@ember/object';
import generateBlueprintFields from 'conduit-ui/utils/blueprints/generate-blueprint-fields';

export function generateBlueprintValidations(type, value) {
  return {
    type,
    value,
  };
}

export function generateBlueprint(
  id,
  label,
  description,
  type,
  validationOpts = {}
) {
  let validations = [];

  if (validationOpts.isRequired) {
    const requiredValidation = generateBlueprintValidations(
      'TYPE_REQUIRED',
      ''
    );
    validations = [requiredValidation];
  }

  if (validationOpts.validations) {
    validations = [...validations, ...validationOpts.validations];
  }

  const blueprint = {};

  blueprint[id] = {
    id,
    label,
    description,
    type,
    validations,
  };

  return blueprint;
}

export function generateBlankBlueprintField(
  id,
  label,
  description,
  type,
  validationOpts = {}
) {
  const blueprint = generateBlueprint(
    id,
    label,
    description,
    type,
    validationOpts
  );

  const configurable = EmberObject.create();
  const blueprinted = EmberObject.create({
    blueprint: [blueprint],
  });

  const blueprintFields = generateBlueprintFields(blueprinted, configurable);

  return blueprintFields.firstObject;
}
