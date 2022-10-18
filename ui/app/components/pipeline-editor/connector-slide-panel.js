import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';
import lookupValidator from 'ember-changeset-validations';
import { Changeset } from 'ember-changeset';
import { run } from '@ember/runloop';

import { validatePresence } from 'ember-changeset-validations/validators';

const ConnectorTransformValidations = {
  name: validatePresence({ presence: true }),
};

export default class PipelineEditorConnectorSlidePanel extends Component {
  @service
  store;

  @tracked
  isShowingAvailableTransforms = false;

  @tracked
  isShowingNewTransformPanel = false;

  @tracked
  isShowingEditTransformPanel = false;

  @tracked
  selectedTransform = null;

  @tracked
  draggingItem = null;

  get sortedTransforms() {
    const processors = this.args.selectedNode.get('processors.content');
    return processors
      ? this.args.selectedNode.get('processors').sortBy('ordinal')
      : [];
  }

  get wrappedSortedTransforms() {
    return this.sortedTransforms.map((processor) => {
      return Changeset(
        processor,
        lookupValidator(ConnectorTransformValidations),
        ConnectorTransformValidations,
        {
          changesetKeys: ['type', 'config', 'parent'],
        }
      );
    });
  }

  @action
  showNewTransformPanel(transform) {
    const newConnectorTransform = this.store.createRecord('processor', {
      type: `${transform.id}${transform.onOptions.firstObject}`,
      parent: {
        type: 'TYPE_CONNECTOR',
        id: this.args.selectedNode.id,
      },
      config: { settings: {} },
    });

    this.selectedTransform = Changeset(
      newConnectorTransform,
      lookupValidator(ConnectorTransformValidations),
      ConnectorTransformValidations,
      {
        changesetKeys: ['type', 'config', 'parent', 'transform'],
      }
    );

    this.isShowingNewTransformPanel = true;
  }

  @action
  cancelNewTransformPanel() {
    this.selectedTransform = null;
    this.isShowingNewTransformPanel = false;
  }

  @action
  showEditTransformPanel(connectorTransform) {
    this.selectedTransform = connectorTransform;
    this.isShowingEditTransformPanel = true;
  }

  @action
  cancelEditTransformPanel() {
    this.isShowingEditTransformPanel = false;
    this.selectedTransform = null;
  }

  @action
  sortConnectorTransforms(connectorTransforms) {
    run(() => {
      connectorTransforms.forEach((connectorTransform, idx) => {
        if (connectorTransform.ordinal !== idx) {
          connectorTransform.data.hasPendingChanges = true;
        }
        connectorTransform.ordinal = idx;
      });
    });

    if (connectorTransforms.isAny('data.hasPendingChanges')) {
      this.args.selectedNode.data.hasPendingChanges = true;
    }
  }

  @action
  async addTransform(connectorTransform) {
    await connectorTransform.save();
    this.args.selectedNode.processors.pushObject(connectorTransform.data);
    this.isShowingNewTransformPanel = false;
    this.isShowingAvailableTransforms = false;
  }

  @action
  async updateTransform(connectorTransform) {
    await connectorTransform.save();
    this.isShowingEditTransformPanel = false;
  }

  @action
  duplicateTransform(connectorTransform) {
    const duplicated = this.store.createRecord('processor', {
      type: connectorTransform.type,
      parent: connectorTransform.data.parent,
      config: connectorTransform.data.config,
    });

    const changeset = Changeset(
      duplicated,
      lookupValidator(ConnectorTransformValidations),
      ConnectorTransformValidations,
      {
        changesetKeys: ['type', 'config', 'parent', 'transform'],
      }
    );

    this.addTransform(changeset);
    this.isShowingEditTransformPanel = false;
  }

  @action
  async removeTransform(connectorTransform) {
    await connectorTransform.data.destroyRecord();
    this.selectedTransform = null;

    this.isShowingEditTransformPanel = false;
    this.isShowingAvailableTransforms = false;
  }

  @action
  onDragStart(item) {
    this.draggingItem = item;
  }

  @action
  onDragStop() {
    this.draggingItem = null;
  }
}
