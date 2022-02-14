import Component from '@glimmer/component';
import { inject as service } from '@ember/service';

export default class PipelineEditorNodesStreamNodeComponent extends Component {
  @service pipelineNodeManager;

  registerNode(element, [, pipelineNodeManager]) {
    pipelineNodeManager.registerStreamNode(element);
  }

  unregisterNode(element, [, pipelineNodeManager]) {
    pipelineNodeManager.unregisterStreamNode(element);
  }
}
