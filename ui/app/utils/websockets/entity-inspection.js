import { tracked } from '@glimmer/tracking';
import { prettyPrintJson } from 'pretty-print-json';
import { htmlSafe } from '@ember/template';
import { run } from '@ember/runloop';

export default class EntityInspection {
  id = null;

  @tracked
  _records = [];

  @tracked
  isTrackingRecords = true;

  constructor(id, socket) {
    this.id = id;
    this._socket = socket;

    this._socket.addEventListener('message', (event) => {
      if (!this.isTrackingRecords) {
        return;
      }

      const data = JSON.parse(event.data);

      run(() => {
        if (this._records.length >= 10) {
          this._records.shiftObject();
        }
        if (data.result !== 'null') {
          this._records.pushObject(
            htmlSafe(prettyPrintJson.toHtml(data.result))
          );
        }
      });
    });
  }

  trackRecords(shouldTrackRecords = true) {
    this.isTrackingRecords = !!shouldTrackRecords;
  }

  get hasRecords() {
    return this._records.length > 0;
  }

  get records() {
    return this._records;
  }
}
