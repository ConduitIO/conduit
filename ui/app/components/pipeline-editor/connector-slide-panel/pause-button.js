import Component from '@glimmer/component';

const pauseMap = {
  running: 'text-gray-500',
  degraded: 'text-gray-500',
  paused: 'text-yellow-400',
  pending: 'text-yellow-400',
};

export default class PipelineEditorConnectorSlidePanelPauseButtonComponent extends Component {
  get textColorClass() {
    return pauseMap[this.args.connectorStatus];
  }

  get isDisabled() {
    return (
      this.args.connectorStatus === 'paused' ||
      this.args.connectorStatus === 'pending'
    );
  }
}
