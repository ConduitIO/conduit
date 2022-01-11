import Controller from '@ember/controller';
import { action } from '@ember/object';

export default class PipelineSettingsController extends Controller {
  @action
  async savePipeline(changeset) {
    const pipeline = changeset.save();

    try {
      await pipeline;
    } catch (error) {
      return;
    }
    this.replaceRoute('pipeline', pipeline.id);
  }
}
