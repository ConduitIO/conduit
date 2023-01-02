import Controller from '@ember/controller';
import config from 'conduit-ui/config/environment';

export default class ApplicationController extends Controller {
  apiURL = config.conduitAPIURL;
}
