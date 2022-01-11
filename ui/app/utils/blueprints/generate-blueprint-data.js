import EmberObject from '@ember/object';
import generateBlueprintFields from 'conduit-ui/utils/blueprints/generate-blueprint-fields';

export function generateBlueprintValidations(type, params, opts) {
  const validation = {
    type,
    params,
  };

  if (opts) {
    validation.options = opts;
  }

  return validation;
}

export function generateBlueprint(
  id,
  label,
  placeholder,
  type,
  validationOpts = {}
) {
  let validations = [];

  if (validationOpts.isRequired) {
    const requiredValidation = generateBlueprintValidations(
      'required',
      'this field is required'
    );
    validations = [requiredValidation];
  }

  if (validationOpts.validations) {
    validations = [...validations, ...validationOpts.validations];
  }

  return {
    id,
    label,
    placeholder,
    type,
    validations,
  };
}

export function generateBlankBlueprintField(
  id,
  label,
  placeholder,
  type,
  validationOpts = {}
) {
  const blueprint = generateBlueprint(
    id,
    label,
    placeholder,
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
