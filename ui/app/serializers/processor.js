import ApplicationSerializer from './application';

const PARENT_TYPE_MAP = {
  TYPE_CONNECTOR: 'connector',
  TYPE_PIPELINE: 'pipeline',
};

export default class ProcessorSerializer extends ApplicationSerializer {
  attrs = {
    connector: {
      serialize: false,
    },
  };

  serialize(snapshot, options) {
    const serialized = super.serialize(snapshot, options);

    const serializedConfigSettings = Object.keys(
      serialized.config.settings,
    ).reduce((acc, settingsKey) => {
      acc[settingsKey.replace('@@', '.')] =
        serialized.config.settings[settingsKey];
      return acc;
    }, {});

    serialized.config.settings = serializedConfigSettings;

    return serialized;
  }

  normalize(typeClass, hash) {
    if (hash.config?.settings) {
      hash.config.settings = Object.keys(hash.config.settings).reduce(
        (acc, settingsKey) => {
          acc[settingsKey.replace('.', '@@')] =
            hash.config.settings[settingsKey];
          return acc;
        },
        {},
      );
    }

    return super.normalize(typeClass, hash);
  }

  extractRelationships(modelClass, hash) {
    if (!hash.parent) {
      return {};
    }

    const relationshipName = PARENT_TYPE_MAP[hash.parent.type];

    return {
      [relationshipName]: {
        data: {
          type: relationshipName,
          id: hash.parent.id,
        },
      },
    };
  }
}
