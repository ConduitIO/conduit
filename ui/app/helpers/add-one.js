import { helper } from '@ember/component/helper';

function addOne(args) {
  let [index] = args;

  return index + 1;
}

export default helper(addOne);
