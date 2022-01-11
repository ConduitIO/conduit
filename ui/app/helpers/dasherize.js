import { helper } from '@ember/component/helper';
import { dasherize as dasherizeStr } from '@ember/string';

function dasherize([text]) {
  return dasherizeStr(text);
}

export default helper(dasherize);
