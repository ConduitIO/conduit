import Route from '@ember/routing/route';
import { Changeset } from 'ember-changeset';
import lookupValidator from 'ember-changeset-validations';
import {
  validatePresence,
  validateLength,
} from 'ember-changeset-validations/validators';

const PipelineValidations = {
  name: validatePresence({ presence: true }),
  description: validateLength({ max: 250 }),
};

export default class PipelineSettingsRoute extends Route {
  model() {
    const pipeline = this.modelFor('pipeline').pipeline;

    return Changeset(
      pipeline,
      lookupValidator(PipelineValidations),
      PipelineValidations,
      {
        changesetKeys: ['name', 'description', 'tags'],
      }
    );
  }

  afterModel(model) {
    model.validate();
  }
}
