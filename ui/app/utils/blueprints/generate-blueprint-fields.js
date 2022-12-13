import { Changeset } from 'ember-changeset';
import lookupValidator from 'ember-changeset-validations';
import {
  validatePresence,
  validateNumber,
  validateInclusion,
  validateExclusion,
  validateFormat,
} from 'ember-changeset-validations/validators';
import { underscore } from '@ember/string';

const ConfigValidationMap = {
  TYPE_REQUIRED: function () {
    return validatePresence(true);
  },

  TYPE_GREATER_THAN: function (value, type) {
    const parseFn = type === 'TYPE_INT' ? parseInt : parseFloat;
    const options = { gt: parseFn(value) };
    return validateNumber(options);
  },

  TYPE_LESS_THAN: function (value, type) {
    const parseFn = type === 'TYPE_INT' ? parseInt : parseFloat;
    const options = { lt: parseFn(value) };
    return validateNumber(options);
  },

  TYPE_INCLUSION: function (value = '') {
    const options = { list: value.split(',') };
    return validateInclusion(options);
  },

  TYPE_EXCLUSION: function (value = '') {
    const options = {
      list: value.split(','),
      message: '{description} cannot be any of ({list})',
    };
    return validateExclusion(options);
  },

  TYPE_REGEX: function (value) {
    const options = {
      regex: new RegExp(value),
      message: '{description} must match regex {regex}',
    };
    return validateFormat(options);
  },
};

export default function generateBlueprintFields(blueprint, configurable) {
  const fieldNames = Object.keys(blueprint);

  return fieldNames.map((fieldName) => {
    const fieldOpts = blueprint[fieldName];
    const currentConfig = configurable.get(`config.settings.${fieldName}`);
    const currentConfigValue = currentConfig ? currentConfig : null;

    const validations = generateConfigValidations(
      fieldOpts.validations,
      fieldOpts.type
    );

    const fieldModel = {
      id: fieldName,
      label: underscore(fieldName).replace('@@', '_').split('_').join(' '),
      description: fieldOpts.description,
      type: fieldOpts.type,
      isRequired: !!fieldOpts.validations.findBy('type', 'TYPE_REQUIRED'),
      rawValidations: fieldOpts.validations,
      value: currentConfigValue,
      hasUserTakenAction: false,
    };

    return Changeset(fieldModel, lookupValidator(validations), validations);
  });
}

function generateConfigValidations(fieldValidations) {
  const validations = fieldValidations.map((validation) => {
    return ConfigValidationMap[validation.type](validation.value);
  });

  return {
    value: validations,
  };
}
