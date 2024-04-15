import RESTAdapter from '@ember-data/adapter/rest';
import { dasherize } from '@ember/string';
import { pluralize } from 'ember-inflector';
import config from 'conduit-ui/config/environment';
import { inject as service } from '@ember/service';
import { InvalidError } from '@ember-data/adapter/error';

export default class ApplicationAdapter extends RESTAdapter {
  @service
  flashMessages;

  namespace = 'v1';
  host = config.conduitAPIURL;

  pathForType(modelName) {
    var dasherized = dasherize(modelName);
    return pluralize(dasherized);
  }

  handleResponse(status, headers, payload, requestData) {
    if (this.isSuccess(status, headers, payload)) {
      return payload;
    } else if (this.isInvalid(status, headers, payload)) {
      return new InvalidError(
        typeof payload === 'object' ? payload.details : undefined,
      );
    }

    const handledResponse = super.handleResponse(
      status,
      headers,
      payload,
      requestData,
    );

    if (handledResponse.isAdapterError) {
      this.flashMessages.add({
        type: 'error',
        message: payload.message || 'Server Error',
        sticky: true,
      });
    }
    return handledResponse;
  }

  normalizeErrorResponse(status, headers, payload) {
    if (
      payload &&
      typeof payload === 'object' &&
      payload.errors instanceof Array
    ) {
      return payload.errors;
    } else {
      return [
        {
          status: `${status}`,
          title: 'The backend responded with an error',
          detail: payload.message,
        },
      ];
    }
  }
}
