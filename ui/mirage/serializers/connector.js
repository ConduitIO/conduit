import ApplicationSerializer from './application';

export default ApplicationSerializer.extend({
  alwaysIncludeLinkageData: true,
  typeKey: 'connector',
});
