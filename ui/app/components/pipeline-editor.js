import Component from '@glimmer/component';
import { action } from '@ember/object';
import { tracked } from '@glimmer/tracking';
import { later, run } from '@ember/runloop';
import { inject as service } from '@ember/service';
import { A } from '@ember/array';
import { Changeset } from 'ember-changeset';
import lookupValidator from 'ember-changeset-validations';
import { isChangeset } from 'validated-changeset';
import { validatePresence } from 'ember-changeset-validations/validators';
/**
 * PLEASE READ THIS NOTE ABOUT CHANGESETS
 *
 * It turns out, ember-changeset does some magic proxy trapping on "get" and "set" when values
 * are set on objects. Also remember ember-data records are essentially global singletons.
 *
 * Enter the ember-inspector. The ember-inspector does some sort of mutation on ember-data
 * objects, I believe around events for teardown. This can actually set strange "willDestroy"
 * properties on the changesets. This is not great...
 *
 * Changeset actually accounts for this via the 'changesetKeys' option we pass into new changesets
 * but unfortunately it doesnt work for ember-data objects that have related ember-data objects...
 * it registers in changeset as a nested change because the related ember-data model is being mutated
 * by the inspector
 *
 * SEE https://github.com/poteto/ember-changeset/issues/485
 *
 * TLDR simply opening the ember-inspector can mess with changesets, thus making debugging
 * a nightmare, and messing with your beautiful dashboard experience.
 *
 * This should never affect any regular users, but could make developing against conduit UI a bad experience.
 * I have not seen this issue after cleaning some things up, but it needs a more smoke testing before we can fully rely on 'changes'
 */

const ConnectorValidations = {
  name: validatePresence({ presence: true }),
  plugin: validatePresence({ presence: true }),
};

export default class PipelineEditorComponent extends Component {
  @service
  pipelineNodeManager;

  @service
  websockets;

  @service
  store;

  @tracked
  isEditingPipeline = false;

  @tracked
  selectedNode = null;

  @tracked
  newConnectorType = 'source';

  @tracked
  isShowingNewConnectorModal = false;

  @tracked
  isShowingEditConnectorModal = false;

  @tracked
  isShowingDeleteConnectorModal = false;

  @tracked
  isShowingConfirmPlayModal = false;

  @tracked
  isShowingConfirmPauseModal = false;

  @tracked
  isShowingStreamInspector = false;

  @tracked
  wrappedConnectors = A([]);

  @tracked
  wrappedConnectorTransforms = A([]);

  @tracked
  pendingConnectors = A([]);

  @tracked
  connectorModalModel = null;

  constructor() {
    super(...arguments);
    this._wrapConnectors(this.args.pipeline);
  }

  // Wrap all connectors in changesets
  // This lets us validate and queue up changes
  // without affecting the underlying model
  _wrapConnectors(pipeline) {
    this.wrappedConnectors = pipeline.get('connectors').map((connector) => {
      return Changeset(
        connector,
        lookupValidator(ConnectorValidations),
        ConnectorValidations,
        {
          changesetKeys: [
            'name',
            'plugin',
            'type',
            'config',
            'connectorPlugin',
            'pipeline',
          ],
        }
      );
    });
  }

  get sourceConnectors() {
    let connectors;
    if (this.pendingConnectors.length > 0) {
      connectors = [...this.wrappedConnectors, ...this.pendingConnectors];
    } else {
      connectors = this.wrappedConnectors;
    }

    return connectors.filterBy('type', 'source');
  }

  get destinationConnectors() {
    let connectors;
    if (this.pendingConnectors.length > 0) {
      connectors = [...this.wrappedConnectors, ...this.pendingConnectors];
    } else {
      connectors = this.wrappedConnectors;
    }

    return connectors.filterBy('type', 'destination');
  }

  get allConnectors() {
    return A([...this.sourceConnectors, ...this.destinationConnectors]);
  }

  get isConnectorSelectedNode() {
    if (!this.selectedNode) {
      return false;
    }

    return (
      isChangeset(this.selectedNode) &&
      this.selectedNode.data.constructor.modelName === 'connector'
    );
  }

  get isLongConnectorName() {
    return this.selectedNode?.name.length > 32;
  }

  @action
  showCreateConnectorModal(connectorType) {
    this.newConnectorType = connectorType;

    const connectorModel = this.store.createRecord('connector', {
      type: this.newConnectorType,
      plugin: '',
      config: { name: '', settings: {} },
      pipeline: this.args.pipeline,
    });

    this.connectorModalModel = Changeset(
      connectorModel,
      lookupValidator(ConnectorValidations),
      ConnectorValidations,
      {
        changesetKeys: [
          'name',
          'type',
          'plugin',
          'config',
          'connectorPlugin',
          'pipeline',
        ],
      }
    );
    this.isShowingNewConnectorModal = true;
  }

  @action
  cancelCreateConnectorModal() {
    if (this.connectorModalModel) {
      this.connectorModalModel.data.unloadRecord();
    }
    this.connectorModalModel = null;
    this.isShowingNewConnectorModal = false;
  }

  @action
  showEditConnectorModal(connector) {
    this.isShowingEditConnectorModal = true;
    this.connectorModalModel = connector;
  }

  @action
  cancelEditConnectorModal() {
    this.connectorModalModel.rollbackProperty('name');
    this.connectorModalModel.rollbackProperty('pluginPath');
    this.connectorModalModel.rollbackProperty('connectorType');
    this.connectorModalModel.rollbackProperty('settings');

    this.connectorModalModel = null;
    this.isShowingEditConnectorModal = false;
  }

  @action
  async createConnector(connector) {
    try {
      await connector.save();
    } catch (error) {
      return;
    }

    this.refreshEditorView(this.wrappedConnectors, connector);
    this.isShowingNewConnectorModal = false;
  }

  @action
  async updateConnector(connector) {
    try {
      await connector.save();
    } catch (error) {
      return;
    }

    this.connectorModalModel = null;
    this.isShowingEditConnectorModal = false;
  }

  @action
  async rollbackConnectorChanges(connector) {
    connector.connectorTransforms.invoke('rollback');
    connector.rollback();
  }

  @action
  async destroyConnector(connector) {
    const connectorId = connector.id;
    await connector.data.destroyRecord();

    this.wrappedConnectors.removeObject(
      this.wrappedConnectors.findBy('id', connectorId)
    );
    this.pipelineNodeManager.regeneratePaths();
    this.selectNode(null);
    this.hideDeleteConnectorModal();
  }

  @action
  selectNode(node, event) {
    later(
      this,
      function () {
        this.selectedNode = node;
      },
      10
    );

    if (event) {
      event.stopPropagation();
    }
  }

  @action
  showDeleteConnectorModal() {
    this.isShowingDeleteConnectorModal = true;
  }

  @action
  hideDeleteConnectorModal() {
    this.isShowingDeleteConnectorModal = false;
  }

  unregister(element, [pipelineNodeManager]) {
    pipelineNodeManager.unregisterNodes();
  }

  refreshEditorView(recordList, record) {
    // Run loops to the rescue
    // It might be less fragile to instead explicitly create/destroy stream nodes
    //
    // This executes the actual changes to the node in one run loop
    // and then regenerates the svg paths in another.
    // This lets us wait for the stream node to insert itself onto
    // the editor before we attempt to regenerate the paths
    run(() => {
      recordList.pushObject(record);
    });

    run(() => {
      this.pipelineNodeManager.regeneratePaths();
    });
  }

  willDestroy() {
    super.willDestroy(...arguments);

    this.cancelCreateConnectorModal();
  }
}
