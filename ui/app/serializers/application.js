import JSONSerializer from '@ember-data/serializer/json';

export default class ApplicationSerializer extends JSONSerializer {
  _replaceKeys(obj, replace, replaceWith) {
    return Object.keys(obj).reduce((acc, key) => {
      const replacedKey = key.replace(replace, replaceWith);
      acc[replacedKey] = obj[key];

      return acc;
    }, {});
  }
}
