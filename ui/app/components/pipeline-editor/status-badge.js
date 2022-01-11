import Component from '@glimmer/component';

const bgColorMap = {
  running: 'bg-teal-600',
  paused: 'bg-gray-500',
  degraded: 'bg-orange-700',
};

export default class PipelineEditorStatusBadgeComponent extends Component {
  get backgroundColorClass() {
    return bgColorMap[this.args.status];
  }
}
