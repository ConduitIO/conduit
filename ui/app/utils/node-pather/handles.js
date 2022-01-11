import { tracked } from '@glimmer/tracking';

export class InputHandle {
  @tracked element;

  constructor(element) {
    this.element = element;
  }

  get xPos() {
    return this.element.offsetLeft;
  }

  get yPos() {
    return this.element.offsetTop + (this.element.offsetHeight / 2);
  }
}

export class OutputHandle {
  @tracked element;

  constructor(element) {
    this.element = element;
  }

  get xPos() {
    return this.element.offsetLeft + this.element.offsetWidth;
  }

  get yPos() {
    return this.element.offsetTop + (this.element.offsetHeight / 2);
  }
}
