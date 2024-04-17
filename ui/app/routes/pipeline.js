import Route from '@ember/routing/route';
import { Changeset } from 'ember-changeset';
import lookupValidator from 'ember-changeset-validations';
import {
  validatePresence,
  validateLength,
} from 'ember-changeset-validations/validators';
import { inject as service } from '@ember/service';

const PipelineValidations = {
  name: validatePresence({ presence: true }),
  description: validateLength({ max: 250 }),
};

export default class PipelineRoute extends Route {
  @service
  store;

  async model(params) {
    let pipeline;
    if (params.pipeline_id === 'new') {
      pipeline = this.store.createRecord('pipeline', { config: {} });
      pipeline = Changeset(
        pipeline,
        lookupValidator(PipelineValidations),
        PipelineValidations,
        {
          changesetKeys: ['name', 'description'],
        },
      );
      pipeline.validate();
    } else {
      pipeline = await this.store.findRecord('pipeline', params.pipeline_id, {
        reload: true,
      });
      await this.store.findAll('plugin');
      await pipeline.hasMany('connectors').reload();

      const connectorIDs = pipeline.connectors.mapBy('id');
      const parentIDs = [pipeline.id, ...connectorIDs];

      await this.store.query('processor', { parent_ids: parentIDs });
    }

    return {
      pipeline,
    };
  }
}
