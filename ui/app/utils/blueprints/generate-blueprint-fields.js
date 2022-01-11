import { Changeset } from 'ember-changeset';
import lookupValidator from 'ember-changeset-validations';
import {
  validatePresence,
  validateLength,
  validateNumber,
  validateInclusion,
  validateExclusion,
  validateFormat,
} from 'ember-changeset-validations/validators';

const ConfigValidationMap = {
  required: function (options) {
    return validatePresence(options);
  },

  number: function (options) {
    return validateNumber(options);
  },

  length: function (options) {
    return validateLength(options);
  },

  inclusion: function (options) {
    return validateInclusion(options);
  },

  exclusion: function (options) {
    return validateExclusion(options);
  },

  format: function (options) {
    return validateFormat(options);
  },
};

export default function generateBlueprintFields(blueprinted, configurable) {
  const blueprint = blueprinted.blueprint;

  return blueprint.map((field) => {
    const currentConfig = configurable.get(`config.settings.${field.id}`);
    const currentConfigValue = currentConfig ? currentConfig : null;

    const validations = generateConfigValidations(field.validations);

    const fieldModel = {
      id: field.id,
      label: field.label,
      placeholder: field.placeholder,
      type: field.type,
      isRequired: !!field.validations.findBy('type', 'required'),
      rawValidations: field.validations,
      value: currentConfigValue,
      hasUserTakenAction: false,
    };

    return Changeset(fieldModel, lookupValidator(validations), validations);
  });
}

function generateConfigValidations(fieldValidations) {
  const validations = fieldValidations.map((validation) => {
    return ConfigValidationMap[validation.type](validation.options || true);
  });

  return {
    value: validations,
  };
}
