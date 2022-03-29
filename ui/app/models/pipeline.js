import Model, { attr, hasMany } from '@ember-data/model';
import axios from 'axios';
import config from 'conduit-ui/config/environment';
import Ember from 'ember';
import { task, timeout } from 'ember-concurrency';

const STATUS_MAP = {
  STATUS_STOPPED: 'paused',
  STATUS_DEGRADED: 'degraded',
  STATUS_RUNNING: 'running',
};

const API_URL = config.conduitAPIURL ? config.conduitAPIURL : '';

export default class PipelineModel extends Model {
  @attr()
  config;

  @attr()
  state;

  @attr()
  connectorIds;

  @hasMany('connector')
  connectors;

  get name() {
    return this.config.name;
  }

  set name(newName) {
    this.config.name = newName;
  }

  get description() {
    return this.config.description;
  }

  set description(newDescription) {
    this.config.description = newDescription;
  }

  get connectorCount() {
    return this.connectorIds.length;
  }

  get humanFriendlyStatus() {
    return this.state.status ? STATUS_MAP[this.state.status] : null;
  }

  get humanFriendlyStatusError() {
    return this.state.error;
  }

  get isDegraded() {
    return this.state.status === 'STATUS_DEGRADED';
  }

  get isRunning() {
    return this.state.status === 'STATUS_RUNNING';
  }

  get isPaused() {
    return this.isDegraded || this.state.status === 'STATUS_STOPPED';
  }

  async startPipeline() {
    await axios.post(`${API_URL}/v1/pipelines/${this.id}/start`);
  }

  async stopPipeline() {
    await axios.post(`${API_URL}/v1/pipelines/${this.id}/stop`);
  }

  @task
  *pollPipeline() {
    let interval = Ember.testing ? 100 : 1000;
    while (this.isRunning) {
      yield timeout(interval);
      yield this.reload();
      if (this.isDegraded) {
        this.triggerPipelineEvent('onPipelineDegraded', this);
        return;
      }
      if (this.isPaused) {
        return;
      }
      if (Ember.testing) {
        return;
      }
    }
  }

  events = {};

  onPipelineEvent(eventName, ctx, fn) {
    this.events[eventName] = { ctx, fn };
  }

  triggerPipelineEvent(eventName, ...args) {
    const ctx = this.events[eventName].ctx;
    const fn = this.events[eventName].fn;

    fn.apply(ctx, args);
  }
}
