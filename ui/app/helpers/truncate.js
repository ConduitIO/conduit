import { helper } from '@ember/component/helper';

function truncate([text]) {
  if (text) {
    if (text.length > 40) {
      return `${text.slice(0,40)}...`;
    } else {
      return text;
    }
  } else {
    return '';
  }
}

export default helper(truncate);
