import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';

export default class PipelineEditorConfigFieldComponent extends Component {
  @tracked
  selectSelected;

  selectOptions;

  get isFieldInvalid() {
    return this.args.field.hasUserTakenAction && !this.args.field.isValid;
  }

  get isFieldValid() {
    return !this.isFieldInvalid;
  }

  validateField() {
    this.args.field.validate();
  }

  constructor() {
    super(...arguments);

    this.fieldType = this.generateFieldType();

    if (this.fieldType === 'select') {
      this.selectOptions = this.generateSelectOptions();
      this.selectSelected = this.selectOptions.firstObject;
    }
  }

  generateFieldType() {
    const field = this.args.field;

    const isString = field.type === 'string' || field.type === 'class';

    const isNumber = field.type === 'int' || field.type === 'long';

    const isPassword = field.type === 'password';

    // TODO clean this up, and also check for special case of number, but using inclusion validation
    // for numbers in a list?
    const isTextInput =
      isString && !field.rawValidations.findBy('type', 'inclusion');
    const isNumberInput =
      isNumber && !field.rawValidations.findBy('type', 'inclusion');
    const isPasswordInput = isPassword;
    const isToggleInput = field.type == 'boolean';
    const isSelectInput =
      (isString || isNumber) &&
      field.rawValidations.findBy('type', 'inclusion');

    if (isTextInput) {
      return 'text';
    }

    if (isNumberInput) {
      return 'number';
    }

    if (isPasswordInput) {
      return 'password';
    }

    if (isToggleInput) {
      return 'toggle';
    }

    if (isSelectInput) {
      return 'select';
    }
  }

  generateSelectOptions() {
    const inclusionValidation = this.args.field.rawValidations.findBy(
      'type',
      'inclusion'
    );
    return inclusionValidation.options.list.map((item) => {
      return {
        name: item,
        value: item,
      };
    });
  }

  @action
  setInputValue() {
    if (!this.args.field.hasUserTakenAction) {
      this.args.field.hasUserTakenAction = true;
    }
    this.args.setInputValue(...arguments);
  }

  @action
  setSelect(changeset, selection) {
    this.args.setInputValue(changeset, { target: { value: selection.value } });
    this.selectSelected = selection;
  }

  @action
  setToggle(changeset, event) {
    this.args.setInputValue(changeset, {
      target: { value: event.target.checked },
    });
  }
}
