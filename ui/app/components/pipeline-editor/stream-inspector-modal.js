import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { inject as service } from '@ember/service';

export default class StreamInspectorModal extends Component {
  @service
  websockets;

  @tracked
  isShowingSingleRecord = false;

  get inspector() {
    return this.websockets.getEntityInspection(this.args.entityId);
  }

  get isStreaming() {
    return this.inspector.isTrackingRecords;
  }

  get isShowingControls() {
    return this.inspector.hasRecords && this.args.pipeline.isRunning;
  }

  @action
  pauseStream() {
    this.inspector.trackRecords(false);
  }

  @action
  resumeStream() {
    this.inspector.trackRecords();
  }
}
