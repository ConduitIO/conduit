import ApplicationSerializer from './application';

const CONNECTOR_TYPE_MAP = {
  TYPE_SOURCE: 'source',
  TYPE_DESTINATION: 'destination',
};

export default class ConnectorSerializer extends ApplicationSerializer {
  serialize(snapshot) {
    const configSettings = this._replaceKeys(
      snapshot.record.config.settings,
      '_',
      '.'
    );

    snapshot.record.config.settings = configSettings;

    return {
      config: snapshot.record.config,
      type: Object.keys(CONNECTOR_TYPE_MAP).find(
        (key) => CONNECTOR_TYPE_MAP[key] === snapshot.record.type
      ),
      plugin: snapshot.record.plugin.get('id'),
      pipeline_id: snapshot.record.pipeline.get('id'),
    };
  }

  extractRelationship(modelName, value) {
    if (modelName === 'plugin' && value) {
      return {
        id: value,
        type: modelName,
      };
    }

    return super.extractRelationship(modelName, value);
  }

  normalize(typeClass, hash) {
    if (hash.config?.settings) {
      const configSettings = this._replaceKeys(hash.config?.settings, '.', '_');
      hash.config.settings = configSettings;
    }

    const normalized = super.normalize(typeClass, hash);
    normalized.data.attributes.type =
      CONNECTOR_TYPE_MAP[normalized.data.attributes.type];
    return normalized;
  }

  _replaceKeys(obj, replace, replaceWith) {
    return Object.keys(obj).reduce((acc, key) => {
      const replacedKey = key.replace(replace, replaceWith);
      acc[replacedKey] = obj[key];

      return acc;
    }, {});
  }
}
