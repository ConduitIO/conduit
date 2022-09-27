import ApplicationSerializer from './application';

const CONNECTOR_TYPE_MAP = {
  TYPE_SOURCE: 'source',
  TYPE_DESTINATION: 'destination',
};

export default class ConnectorSerializer extends ApplicationSerializer {
  serialize(snapshot) {
    const configSettings = super._replaceKeys(
      snapshot.record.config.settings,
      '@@',
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
      const pluginNames = this.store.peekAll('plugin').mapBy('name');

      const hasOnlyType = /^(any|builtin|standalone):[\w-]+$/.test(value);
      const hasOnlyVersion = /^[\w-]+@v\S+$/.test(value);
      const hasTypeAndVersion = /(any|builtin|standalone):[\w-]+@v\S+$/.test(
        value
      );

      let pluginName = value;
      let pluginType = 'any';
      let pluginVersion = 'latest';

      if (hasOnlyType) {
        pluginType = value.split(':')[0];
        pluginName = value.split(':')[1];
      }

      if (hasOnlyVersion) {
        pluginName = value.split('@')[0];
        pluginVersion = value.split('@')[1];
      }

      if (hasTypeAndVersion) {
        const split = value
          .replaceAll(':', '@@')
          .replaceAll('@v', '@@')
          .split('@@');

        pluginType = split[0];
        pluginName = split[1];
        pluginVersion = split[2];
      }

      const filtered = pluginNames.filter((name) => {
        return name.indexOf(pluginName) !== -1;
      });

      if (filtered.length === 1) {
        return {
          id: filtered[0],
          type: modelName,
        };
      }

      const latestVersion = filtered.reduce((acc, name) => {
        const accVersion = acc.split('@v')[1];
        const version = name.split('@v')[1];
        if (accVersion > version) {
          return accVersion;
        } else {
          return version;
        }
      }, '');

      let pluginMatch;

      if (pluginType === 'any') {
        pluginType = 'standalone';
      }

      if (pluginVersion === 'latest') {
        pluginVersion = latestVersion;
      }

      pluginMatch =
        filtered.find((name) => {
          return (
            name.indexOf(`${pluginType}:${pluginName}@${latestVersion}`) !== -1
          );
        }) ||
        filtered.find((name) => {
          return name.indexOf(`builtin:${pluginName}@${latestVersion}`) !== -1;
        });

      if (!pluginMatch) {
        return super.extractRelationship(modelName, value);
      }

      return {
        id: pluginMatch,
        type: modelName,
      };
    }

    return super.extractRelationship(modelName, value);
  }

  normalize(typeClass, hash) {
    if (hash.config?.settings) {
      const configSettings = super._replaceKeys(
        hash.config?.settings,
        '.',
        '@@'
      );
      hash.config.settings = configSettings;
    }

    const normalized = super.normalize(typeClass, hash);
    normalized.data.attributes.type =
      CONNECTOR_TYPE_MAP[normalized.data.attributes.type];
    return normalized;
  }
}
