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
  validationOpts = {},
) {
  let validations = [];

  if (validationOpts.isRequired) {
    const requiredValidation = generateBlueprintValidations(
      'TYPE_REQUIRED',
      '',
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

export function generateBlueprintField(
  id,
  label,
  description,
  type,
  validationOpts = {},
  populatedValue,
) {
  const blueprint = generateBlueprint(
    id,
    label,
    description,
    type,
    validationOpts,
  );

  let configurable;

  if (populatedValue) {
    configurable = EmberObject.create({ config: { settings: {} } });
    configurable.set(`config.settings.${id}`, populatedValue);
  } else {
    configurable = EmberObject.create();
  }

  const blueprintFields = generateBlueprintFields(blueprint, configurable);

  return blueprintFields.firstObject;
}
