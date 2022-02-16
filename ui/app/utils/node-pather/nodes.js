import { tracked } from '@glimmer/tracking';
import { InputHandle, OutputHandle } from './handles';

export class BaseNode {
  @tracked id = null;
  @tracked element = null;

  get width() {
    return this.element.offsetWidth;
  }

  get height() {
    return this.element.offsetHeight;
  }

  get offsetLeft() {
    return this.element.offsetLeft;
  }

  get offsetTop() {
    return this.element.offsetTop;
  }

  constructor(obj) {
    this.id = obj.id;
    this.element = obj.element;
  }
}

export class SourceNode extends BaseNode {
  @tracked outputHandle = null;
  model;

  constructor(obj) {
    super(obj);
    this.model = obj.model;
    this.outputHandle = new OutputHandle(this.element);
  }

  get outputHandleCoords() {
    return `${this.outputHandle.xPos},${this.outputHandle.yPos}`;
  }
}

export class DestinationNode extends BaseNode {
  @tracked inputHandle = null;
  model;

  constructor(obj) {
    super(obj);
    this.model = obj.model;
    this.inputHandle = new InputHandle(this.element);
  }

  get inputHandleCoords() {
    return `${this.inputHandle.xPos},${this.inputHandle.yPos}`;
  }
}

export class StreamNode extends BaseNode {
  @tracked outputHandle = null;
  @tracked inputHandle = null;

  constructor(obj) {
    super(obj);
    this.inputHandle = new InputHandle(this.element);
    this.outputHandle = new OutputHandle(this.element);
  }

  get inputHandleCoords() {
    return `${this.inputHandle.xPos},${this.inputHandle.yPos}`;
  }

  get outputHandleCoords() {
    return `${this.outputHandle.xPos},${this.outputHandle.yPos}`;
  }
}
