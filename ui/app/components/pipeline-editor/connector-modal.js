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
        this.connector.connectorPlugin,
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
    return !!this.connector.connectorPlugin;
  }

  get selectedConnectorPlugin() {
    if (this.connector.connectorPlugin) {
      return this.connectorPluginOptions.findBy(
        'id',
        this.connector.get('connectorPlugin.id')
      );
    } else {
      return this.connectorPluginDefault;
    }
  }

  get sourceConnectorPlugins() {
    return this.args.connectorPlugins.filter((plugin) => {
      return plugin.connectorType === 'source';
    });
  }

  get destinationConnectorPlugins() {
    return this.args.connectorPlugins.filter((plugin) => {
      return plugin.connectorType === 'destination';
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
      this.connector.data.plugin = '';
      this.connector.validate();
      this.blueprintFields = [];
    } else {
      this.connector.data.plugin = plugin.pluginPath;
      this.connector.validate();
      this.blueprintFields = generateBlueprintFields(plugin, this.connector);
    }
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
