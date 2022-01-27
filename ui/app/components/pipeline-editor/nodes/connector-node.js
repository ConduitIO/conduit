import Component from '@glimmer/component';
import { inject as service } from '@ember/service';

export default class PipelineEditorNodesConnectorNodeComponent extends Component {
  @service pipelineNodeManager;

  registerNode(element, [nodeType, pipelineNodeManager, model]) {
    if (nodeType === 'source') {
      pipelineNodeManager.registerSourceNode(element, model);
    }

    if (nodeType === 'destination') {
      pipelineNodeManager.registerDestinationNode(element, model);
    }
  }

  unregisterNode(element, [nodeType, pipelineNodeManager]) {
    if (nodeType === 'source') {
      pipelineNodeManager.unregisterSourceNode(element);
    }

    if (nodeType === 'destination') {
      pipelineNodeManager.unregisterDestinationNode(element);
    }
  }

  get isSelected() {
    const selectedNode = this.args.selectedNode;
    if (!selectedNode) {
      return false;
    }
    const connector = this.args.connector;

    return selectedNode === connector;
  }
}
