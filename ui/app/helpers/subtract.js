import { helper } from '@ember/component/helper';

function subtract([x, y]) {
  return x - y;
}

export default helper(subtract);
