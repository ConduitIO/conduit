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

    const isString = field.type === 'TYPE_STRING';

    const isNumber = field.type === 'TYPE_NUMBER';

    // TODO clean this up, and also check for special case of number, but using inclusion validation
    // for numbers in a list?
    const isTextInput =
      isString && !field.rawValidations.findBy('type', 'TYPE_INCLUSION');
    const isNumberInput =
      isNumber && !field.rawValidations.findBy('type', 'TYPE_INCLUSION');
    const isToggleInput = field.type == 'TYPE_BOOL';
    const isSelectInput =
      (isString || isNumber) &&
      field.rawValidations.findBy('type', 'TYPE_INCLUSION');

    if (isTextInput) {
      return 'text';
    }

    if (isNumberInput) {
      return 'number';
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
      'TYPE_INCLUSION'
    );
    return inclusionValidation.value.map((item) => {
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

  @action
  validateField() {
    this.args.field.validate();
  }
}
