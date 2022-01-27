import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';

export default class PipelineFormComponent extends Component {
  @tracked
  isPipelineNameValid = true;

  @action
  setPipelineName(pipeline, event) {
    pipeline.set('name', event.target.value);

    if (pipeline.get('error.name')) {
      this.isPipelineNameValid = false;
    } else {
      this.isPipelineNameValid = true;
    }
  }
}
