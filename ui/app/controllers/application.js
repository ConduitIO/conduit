import Controller from '@ember/controller';
import config from 'conduit-ui/config/environment';
import { inject as service } from '@ember/service';

export default class ApplicationController extends Controller {
  @service
  flashMessages;

  apiURL = config.conduitAPIURL;
}
