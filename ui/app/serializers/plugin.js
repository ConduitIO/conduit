import ApplicationSerializer from './application';

export default class ConnectorPluginSerializer extends ApplicationSerializer {
  primaryKey = 'name';

  normalize(typeClass, hash) {
    hash.sourceParams = super._replaceKeys(hash.sourceParams, '.', '@@');
    hash.destinationParams = super._replaceKeys(
      hash.destinationParams,
      '.',
      '@@',
    );

    return super.normalize(typeClass, hash);
  }
}
