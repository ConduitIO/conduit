import { helper } from '@ember/component/helper';

function getElement([elementId]) {
  return document.getElementById(elementId);
}

export default helper(getElement);
