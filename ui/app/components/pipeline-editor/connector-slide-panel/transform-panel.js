import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import generateBlueprintFields from 'conduit-ui/utils/blueprints/generate-blueprint-fields';
import { capitalize } from '@ember/string';

export default class PipelineEditorConnectorSlidePanelTransformPanel extends Component {
  @tracked
  blueprintFields;

  @tracked
  connectorTransform;

  @tracked
  selectedTransformOnOption;

  constructor() {
    super(...arguments);
    this.connectorTransform = this.args.connectorTransform;
    this.blueprintFields = generateBlueprintFields(
      this.connectorTransform.data.transform.blueprint,
      this.connectorTransform
    );

    this.selectedTransformOnOption = this.transformOnOptions.findBy(
      'value',
      this.connectorTransform.type
    );
  }

  get isValid() {
    return (
      this.connectorTransform.isValid && this.blueprintFields.isEvery('isValid')
    );
  }

  get isEditing() {
    return !!this.args.isEditing;
  }

  get transformOnOptions() {
    return this.connectorTransform.transform.onOptions.map((option) => {
      return {
        name: capitalize(option),
        value: `${this.connectorTransform.transform.id}${option}`,
      };
    });
  }

  @action
  setTransformOnOption(option) {
    this.connectorTransform.type = option.value;
    this.selectedTransformOnOption = this.transformOnOptions.findBy(
      'value',
      option.value
    );
  }

  @action
  setTransformConfig(fieldChangeset, event) {
    fieldChangeset.value = event.target.value;

    if (fieldChangeset.isValid) {
      this.connectorTransform.set(
        `config.settings.${fieldChangeset.id}`,
        fieldChangeset.value
      );
    } else {
      this.connectorTransform.set(`config.${fieldChangeset.id}`, undefined);
    }
  }

  @action
  addTransform() {
    this.args.dismiss();
    this.args.addTransform(...arguments);
  }
}
