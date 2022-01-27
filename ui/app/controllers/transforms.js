import Controller from '@ember/controller';
import { tracked } from '@glimmer/tracking';

export default class TransformsController extends Controller {
  queryParams = ['filter', 'fuzzy', 'page'];

  @tracked
  filter = null;

  @tracked
  fuzzy = null;

  @tracked
  page = null;

  @tracked
  model;

  get filteredTransforms() {
    switch (this.filter) {
      case 'builtin':
        return this.model.filterBy('isCustom', false);
      case 'custom':
        return this.model.filterBy('isCustom', true);
      default:
        return this.model;
    }
  }

  get totalPages() {
    if (this.filteredTransforms.length > 8) {
      return Math.ceil(this.filteredTransforms.length / 8);
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
      return (this.totalPages - 1) * 8 + (this.filteredTransforms.length % 8);
    }
  }

  get currentPageFilteredTransforms() {
    if (this.totalPages > 1) {
      const beginningRange = this.selectedPage * 8 - 8;
      const endingRange = this.currentPageAccumulatedLength;

      return this.filteredTransforms.slice(beginningRange, endingRange);
    } else {
      return this.filteredTransforms;
    }
  }
}
