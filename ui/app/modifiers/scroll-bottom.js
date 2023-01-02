import { modifier } from 'ember-modifier';

export default modifier((element) => {
  element.scrollTop = element.scrollHeight;
  return () => {
    element.scrollTop = 0;
  };
});
