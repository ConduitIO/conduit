import Controller, { inject as controller } from '@ember/controller';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';

export default class PipelinesIndexController extends Controller {
  @controller('pipelines')
  pipelinesController;

  queryParams = ['filter', 'page'];

  @tracked
  filter = null;

  @tracked
  page = null;

  @tracked
  fuzzy = null;

  @tracked
  model;

  get filteredPipelines() {
    const savedModels = this.model.filterBy('isNew', false);
    switch (this.filter) {
      case 'starred':
        return savedModels.filterBy('favorite', true);
      case 'running':
        return savedModels.filterBy('isRunning');
      case 'paused':
        return savedModels.filterBy('isPaused');
      default:
        return savedModels;
    }
  }

  get fuzzyFilteredPipelines() {
    if (!this.fuzzy) {
      return this.filteredPipelines;
    } else {
      return this.filteredPipelines.filter((pipeline) => {
        return (
          pipeline.name.toLowerCase().indexOf(this.fuzzy.toLowerCase()) !== -1
        );
      });
    }
  }

  get totalPages() {
    if (this.filteredPipelines.length > 8) {
      return Math.ceil(this.filteredPipelines.length / 8);
    } else {
      return 1;
    }
  }

  get pagesList() {
    const list = [];
    for (let i = 1; i <= this.totalPages; i++) {
      list.pushObject(i);
    }

    return list;
  }

  get selectedPage() {
    return this.page ? this.page : 1;
  }

  get currentPageAccumulatedLength() {
    if (this.selectedPage < this.totalPages) {
      return this.selectedPage * 8;
    } else {
      return (this.totalPages - 1) * 8 + (this.filteredPipelines.length % 8);
    }
  }

  get currentPageFilteredPipelines() {
    if (this.totalPages > 1) {
      const beginningRange = this.selectedPage * 8 - 8;
      const endingRange = this.currentPageAccumulatedLength;

      return this.fuzzyFilteredPipelines.slice(beginningRange, endingRange);
    } else {
      return this.fuzzyFilteredPipelines;
    }
  }

  @action
  setFuzzy(event) {
    this.fuzzy = event.target.value;
  }
}
