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

  TYPE_GREATER_THAN: function (value, fieldType, isRequired) {
    const parseFn = fieldType === 'TYPE_INT' ? parseInt : parseFloat;
    const options = { gt: parseFn(value), allowBlank: !isRequired };
    return validateNumber(options);
  },

  TYPE_LESS_THAN: function (value, fieldType, isRequired) {
    const parseFn = fieldType === 'TYPE_INT' ? parseInt : parseFloat;
    const options = { lt: parseFn(value), allowBlank: !isRequired };
    return validateNumber(options);
  },

  TYPE_INCLUSION: function (value = '', fieldType, isRequired) {
    const options = { list: value.split(','), allowBlank: !isRequired };
    return validateInclusion(options);
  },

  TYPE_EXCLUSION: function (value = '', fieldType, isRequired) {
    const options = {
      list: value.split(','),
      message: '{description} cannot be any of ({list})',
      allowBlank: !isRequired,
    };
    return validateExclusion(options);
  },

  TYPE_REGEX: function (value, fieldType, isRequired) {
    const options = {
      regex: new RegExp(value),
      message: '{description} must match regex {regex}',
      allowBlank: !isRequired,
    };

    return validateFormat(options);
  },

  TYPE_NUMBER: function (value, fieldType, isRequired) {
    const parseFn = fieldType === 'TYPE_INT' ? parseInt : parseFloat;
    const options = Object.assign(
      { allowBlank: !isRequired },
      { gt: parseFn(value.gt), lt: parseFn(value.lt) }
    );

    return validateNumber(options);
  },
};

export default function generateBlueprintFields(blueprint, configurable) {
  const fieldNames = Object.keys(blueprint);

  return fieldNames.map((fieldName) => {
    const fieldOpts = blueprint[fieldName];
    const currentConfig = configurable.get(`config.settings.${fieldName}`);
    const currentConfigValue = currentConfig ? currentConfig : null;
    const isRequired = !!fieldOpts.validations.findBy('type', 'TYPE_REQUIRED');

    const validations = generateConfigValidations(
      fieldOpts.validations,
      fieldOpts.type,
      isRequired
    );

    const fieldModel = {
      id: fieldName,
      label: underscore(fieldName).replace('@@', '_').split('_').join(' '),
      description: fieldOpts.description,
      type: fieldOpts.type,
      isRequired,
      rawValidations: fieldOpts.validations,
      value: currentConfigValue,
      hasUserTakenAction: false,
    };

    return Changeset(fieldModel, lookupValidator(validations), validations);
  });
}

function generateConfigValidations(fieldValidations, fieldType, isRequired) {
  const gt = fieldValidations.findBy('type', 'TYPE_GREATER_THAN');
  const lt = fieldValidations.findBy('type', 'TYPE_LESS_THAN');

  // Consolidate special case where both gt + lt are present into TYPE_NUMBER
  if (!!gt && !!lt) {
    fieldValidations = fieldValidations.reject((fv) => {
      return fv.type === 'TYPE_GREATER_THAN' || fv.type === 'TYPE_LESS_THAN';
    });

    fieldValidations.pushObject({
      type: 'TYPE_NUMBER',
      value: { gt: gt.value, lt: lt.value },
    });
  }
  const validations = fieldValidations.map((validation) => {
    return ConfigValidationMap[validation.type](
      validation.value,
      fieldType,
      isRequired
    );
  });

  return {
    value: validations,
  };
}
