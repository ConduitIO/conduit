import ApplicationSerializer from './application';

export default class PipelineSerializer extends ApplicationSerializer {
  serialize(snapshot) {
    return {
      config: {
        name: snapshot.record.config.name,
        description: snapshot.record.config.description,
      },
    };
  }

  extractRelationships(modelClass, hash) {
    const id = hash.id;

    return {
      connectors: {
        links: {
          related: `/v1/connectors?pipeline_id=${id}`,
        },
      },
    };
  }
}
