import Controller from '@ember/controller';
import { action } from "@ember/object";
import { tracked } from '@glimmer/tracking';

export default class ConnectorsController extends Controller {
  queryParams = ['filter', 'fuzzy', 'page']

  @tracked
  filter = null;

  @tracked
  fuzzy = null;

  @tracked
  page = null;

  @tracked
  model;

  get filteredConnectors() {
    switch(this.filter) {
      case 'source':
        return this.model.filter((connector) => {
          return connector.pluginType === 'source' || connector.pluginType === 'all';
        });
      case 'destination':
        return this.model.filter((connector) => {
          return connector.pluginType === 'destination' || connector.pluginType === 'all';
        });
      default:
        return this.model;
    }
  }

  get totalPages() {
    if (this.filteredConnectors.length > 8) {
      return Math.ceil(this.filteredConnectors.length / 8);
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
      return ((this.totalPages - 1) * 8) + this.filteredConnectors.length % 8
    }
  }

  get currentPageFilteredConnectors() {
    if (this.totalPages > 1) {
      const beginningRange = this.selectedPage * 8 - 8;
      const endingRange = this.currentPageAccumulatedLength;

      return this.filteredConnectors.slice(beginningRange, endingRange);
    } else {
      return this.filteredConnectors;
    }
  }
}
