import Service from '@ember/service';
import { A } from '@ember/array';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { zoomIdentity } from 'd3-zoom';
import {
  SourceNode,
  DestinationNode,
  StreamNode,
} from 'conduit-ui/utils/node-pather/nodes';
import { QuadraticPath } from 'conduit-ui/utils/node-pather/paths';

export default class PipelineNodeManagerService extends Service {
  @tracked sourceNodes = A([]);
  @tracked streamNode = null;
  @tracked destinationNodes = A([]);

  zoomSelection;
  zoomObject;

  get paths() {
    const source = this.sourceNodes.map((node) => {
      return new QuadraticPath({
        startingNode: node,
        endingNode: this.streamNode,
      });
    });

    const destination = this.destinationNodes.map((node) => {
      return new QuadraticPath({
        startingNode: this.streamNode,
        endingNode: node,
      });
    });

    return [...source, ...destination];
  }

  /* eslint-disable no-self-assign */
  regeneratePaths() {
    this.sourceNodes.forEach((node) => {
      node.outputHandle = node.outputHandle;
    });

    this.destinationNodes.forEach((node) => {
      node.inputHandle = node.inputHandle;
    });

    this.streamNode.outputHandle = this.streamNode.outputHandle;
    this.streamNode.inputHandle = this.streamNode.inputHandle;
  }
  /* eslint-enable no-self-assign */

  registerSourceNode(element, model) {
    const sourceNode = new SourceNode({
      element,
      model,
    });

    this.sourceNodes.pushObject(sourceNode);
  }

  registerStreamNode(element) {
    const streamNode = new StreamNode({
      element,
    });

    this.streamNode = streamNode;
  }

  registerDestinationNode(element, model) {
    const destinationNode = new DestinationNode({
      element,
      model,
    });

    this.destinationNodes.pushObject(destinationNode);
  }

  unregisterSourceNode(element) {
    this.sourceNodes.removeObject(this.sourceNodes.findBy('element', element));
  }

  unregisterDestinationNode(element) {
    this.destinationNodes.removeObject(
      this.destinationNodes.findBy('element', element),
    );
  }

  unregisterNodes() {
    this.sourceNodes = A([]);
    this.destinationNodes = A([]);
    this.streamNode = null;
  }

  unregisterStreamNode() {
    this.streamNode = null;
  }

  @action
  zoomInAction() {
    this.zoomSelection.transition().call(this.zoomObject.scaleBy, 1.4);
  }

  @action
  zoomOutAction() {
    this.zoomSelection.transition().call(this.zoomObject.scaleBy, 0.7);
  }

  @action
  zoomResetAction() {
    this.zoomSelection
      .transition()
      .call(this.zoomObject.transform, zoomIdentity);
  }

  setZoomObject(zoomSelection, zoomObject) {
    this.zoomSelection = zoomSelection;
    this.zoomObject = zoomObject;
  }
}
