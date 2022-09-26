import Controller from '@ember/controller';
import { action } from '@ember/object';
import { isEmpty } from '@ember/utils';
import { inject as service } from '@ember/service';

export default class PipelineSettingsController extends Controller {
  @service
  router;

  get isPipelineUpdateDisabled() {
    const pipeline = this.model;
    return (
      !pipeline.isValid ||
      isEmpty(pipeline.changes) ||
      (!pipeline.isNew && pipeline.isRunning)
    );
  }
  @action
  async savePipeline(changeset) {
    try {
      const pipeline = await changeset.save();
      this.router.transitionTo('pipeline.index', pipeline.id);
    } catch (error) {
      return;
    }
  }
}
