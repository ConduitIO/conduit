import Controller from '@ember/controller';
import { tracked } from '@glimmer/tracking';
import { action } from "@ember/object";

export default class SettingsController extends Controller {
  @tracked
  telemetryOptInLabel = 'No thanks';

  @action
  setTelemetryOptIn(event) {
    event.target.checked ? this.telemetryOptInLabel = "I'm in" : this.telemetryOptInLabel = 'No thanks'
  }
}
