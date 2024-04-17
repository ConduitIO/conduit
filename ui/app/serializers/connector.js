import ApplicationSerializer from './application';
import { inject as service } from '@ember/service';

const CONNECTOR_TYPE_MAP = {
  TYPE_SOURCE: 'source',
  TYPE_DESTINATION: 'destination',
};

export default class ConnectorSerializer extends ApplicationSerializer {
  @service
  store;

  serialize(snapshot) {
    const configSettings = super._replaceKeys(
      snapshot.record.config.settings,
      '@@',
      '.',
    );

    snapshot.record.config.settings = configSettings;

    return {
      config: snapshot.record.config,
      type: Object.keys(CONNECTOR_TYPE_MAP).find(
        (key) => CONNECTOR_TYPE_MAP[key] === snapshot.record.type,
      ),
      plugin: snapshot.record.plugin.get('id'),
      pipeline_id: snapshot.record.pipeline.get('id'),
    };
  }

  _pluginLookup(pluginNames, matchTo) {
    const hasOnlyType = /^(any|builtin|standalone):[\w-]+$/.test(matchTo);
    const hasOnlyVersion = /^[\w-]+@v\S+$/.test(matchTo);
    const hasTypeAndVersion = /^(any|builtin|standalone):[\w-]+@v\S+$/.test(
      matchTo,
    );

    let pluginName = matchTo;
    let pluginType = 'any';
    let pluginVersion = 'latest';

    if (hasOnlyType) {
      pluginType = matchTo.split(':')[0];
      pluginName = matchTo.split(':')[1];
    }

    if (hasOnlyVersion) {
      pluginName = matchTo.split('@v')[0];
      pluginVersion = matchTo.split('@v')[1];
    }

    if (hasTypeAndVersion) {
      const split = matchTo
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
        type: 'plugin',
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
          name.indexOf(`${pluginType}:${pluginName}@v${pluginVersion}`) !== -1
        );
      }) ||
      filtered.find((name) => {
        return name.indexOf(`builtin:${pluginName}@v${pluginVersion}`) !== -1;
      });

    if (!pluginMatch) {
      return super.extractRelationship('plugin', matchTo);
    }

    return {
      id: pluginMatch,
      type: 'plugin',
    };
  }

  extractRelationship(modelName, value) {
    if (modelName === 'plugin' && value) {
      const pluginNames = this.store.peekAll('plugin').mapBy('name');
      return this._pluginLookup(pluginNames, value);
    }

    return super.extractRelationship(modelName, value);
  }

  normalize(typeClass, hash) {
    if (hash.config?.settings) {
      const configSettings = super._replaceKeys(
        hash.config?.settings,
        '.',
        '@@',
      );
      hash.config.settings = configSettings;
    }

    const normalized = super.normalize(typeClass, hash);
    normalized.data.attributes.type =
      CONNECTOR_TYPE_MAP[normalized.data.attributes.type];
    return normalized;
  }
}
