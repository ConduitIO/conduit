import Component from '@glimmer/component';

const playMap = {
  running: 'text-teal-600',
  degraded: 'text-teal-600',
  paused: 'text-gray-500',
  pending: 'text-gray-500',
};

export default class PipelineEditorConnectorSlidePanelPlayButtonComponent extends Component {
  get textColorClass() {
    return playMap[this.args.connectorStatus];
  }

  get isDisabled() {
    return (
      this.args.connectorStatus === 'running' ||
      this.args.connectorStatus === 'degraded' ||
      this.args.connectorStatus === 'pending'
    );
  }
}
