export default function validateExcludeString(opts) {
  return (key, newValue, oldValue, changes, content) => {
    const excluded = opts.list.map((value) => value.toLowerCase());
    if (excluded && excluded.indexOf(newValue.toLowerCase()) !== -1) {
      return 'Names of transforms must be unique';
    }

    return true;
  }
}
