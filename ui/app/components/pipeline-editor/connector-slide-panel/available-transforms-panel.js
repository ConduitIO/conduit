import Component from '@glimmer/component';
import { action } from '@ember/object';
import { tracked } from '@glimmer/tracking';

export default class ConnectorSlidePanelAvailableTransformsPanelComponent extends Component {
  @tracked
  fuzzyInput = '';

  get availableFilteredTransforms() {
    if (this.fuzzyInput) {
      return this.args.availableTransforms.filter((transform) => {
        return (
          transform.name
            .toLowerCase()
            .indexOf(this.fuzzyInput.toLowerCase()) !== -1
        );
      });
    } else {
      return this.args.availableTransforms;
    }
  }

  @action
  onFuzzyInput(event) {
    this.fuzzyInput = event.target.value;
  }
}
