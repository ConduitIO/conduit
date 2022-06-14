import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { A } from '@ember/array';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';
import generateBlueprintFields from 'conduit-ui/utils/blueprints/generate-blueprint-fields';

export default class PipelineEditorNewConnectorModal extends Component {
  get isValid() {
    return this.connector.isValid && this.blueprintFields.isEvery('isValid');
  }

  @service
  pipelineNodeManager;

  @tracked
  selectedConnectorType = {
    name: this.args.newConnectorType,
    value: this.args.newConnectorType,
  };

  @tracked
  connector;

  @tracked
  blueprintFields;

  @tracked
  isShowingRequiredTab = true;

  // Connector name is always valid on init
  // to prevent showing validations until user action is taken.
  @tracked
  isConnectorNameValid = true;

  // static
  connectorPluginDefault = {
    name: 'Please select...',
    id: '',
  };

  // static
  connectorTypes = [
    { name: 'source', value: 'source' },
    { name: 'destination', value: 'destination' },
  ];

  constructor() {
    super(...arguments);
    this.connector = this.args.connector;
    if (this.isEditing) {
      this.blueprintFields = generateBlueprintFields(
        this.connector.plugin.getParams(this.connector.type),
        this.connector
      );
    } else {
      this.blueprintFields = [];
    }
  }

  @action
  validateConnector() {
    this.connector.validate();
  }

  get headerText() {
    if (this.isEditing) {
      return `Edit ${this.connector.name}`;
    } else {
      return this.connector.name ? this.connector.name : 'Name your connector';
    }
  }

  get requiredFields() {
    return this.blueprintFields.filterBy('isRequired', true);
  }

  get optionalFields() {
    return this.blueprintFields.filterBy('isRequired', false);
  }

  get isShowingFields() {
    return !!this.connector.plugin;
  }

  get selectedConnectorPlugin() {
    if (this.connector.plugin) {
      return this.connector.plugin;
    } else {
      return this.connectorPluginDefault;
    }
  }

  get sourceConnectorPlugins() {
    return this.args.connectorPlugins.filter((plugin) => {
      return Object.keys(plugin.sourceParams).length > 0;
    });
  }

  get destinationConnectorPlugins() {
    return this.args.connectorPlugins.filter((plugin) => {
      return Object.keys(plugin.destinationParams).length > 0;
    });
  }

  get connectorPluginOptions() {
    const isNewSource = this.connector.type === 'source';

    const relevantPlugins = isNewSource
      ? this.sourceConnectorPlugins
      : this.destinationConnectorPlugins;
    const defaultOption = this.connectorPluginDefault;

    return A([defaultOption, ...relevantPlugins]);
  }

  get isEditing() {
    return this.args.isEditing;
  }

  @action
  setConnectorName(changeset, event) {
    changeset.set('name', event.target.value);

    if (changeset.get('error.name')) {
      this.isConnectorNameValid = false;
    } else {
      this.isConnectorNameValid = true;
    }
  }

  @action
  setConnectorType(typeOption) {
    this.connector.data.plugin = '';
    this.connector.data.type = typeOption.value;
    this.connector.validate();
    this.blueprintFields = [];
    this.selectedConnectorType = typeOption;
  }

  @action
  setConnectorPlugin(plugin) {
    if (plugin.id === '') {
      this.connector.data.plugin = null;
      this.connector.validate();
      this.blueprintFields = [];
    } else {
      this.connector.data.plugin = plugin;
      this.connector.validate();
      this.blueprintFields = generateBlueprintFields(
        plugin.getParams(this.connector.type),
        this.connector
      );
    }
    this.isShowingRequiredTab = true;
  }

  @action
  setConnectorConfig(fieldChangeset, event) {
    fieldChangeset.value = event.target.value;

    if (fieldChangeset.isValid) {
      this.connector.set(
        `config.settings.${fieldChangeset.id}`,
        fieldChangeset.value
      );
    } else {
      this.connector.set(`config.settings.${fieldChangeset.id}`, undefined);
      // this.connector.rollbackProperty(`config.${fieldChangeset.id}`);
    }
  }
}
