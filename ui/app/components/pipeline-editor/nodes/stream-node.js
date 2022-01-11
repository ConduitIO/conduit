import Component from '@glimmer/component';
import { inject as service } from '@ember/service';

export default class PipelineEditorNodesStreamNodeComponent extends Component {
  @service pipelineNodeManager;

  registerNode(element, [nodeType, pipelineNodeManager, model]) {
    pipelineNodeManager.registerStreamNode(element);
  }

  unregisterNode() {

  }
}
