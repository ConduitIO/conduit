import { tracked } from '@glimmer/tracking';

// A path goes from left to right
// It expects a starting node with an output handle
// and an ending node with an input handle
export default class CubicPath {
  @tracked startingNode = null;
  @tracked endingNode = null;

  constructor(obj) {
    this.startingNode = obj.startingNode;
    this.endingNode = obj.endingNode;
  }

  // X position of halfway between
  // starting and ending nodes
  get halfwayX() {
    const startingNode = this.startingNode;
    const endingNode = this.endingNode;

    return (
      (endingNode.inputHandle.xPos - startingNode.outputHandle.xPos) / 2 +
      startingNode.outputHandle.xPos
    );
  }

  // Y position of the starting node output handle
  get startingNodeOutputHandleY() {
    return this.startingNode.outputHandle.yPos;
  }

  // Y position of the ending node input handle
  get endingNodeInputHandleY() {
    return this.endingNode.inputHandle.yPos;
  }

  // Compute SVG commands
  get svgPath() {
    const startingNode = this.startingNode;
    const endingNode = this.endingNode;

    const startingPoint = `M ${startingNode.outputHandleCoords}`;

    const curve = `C ${this.halfwayX},${this.startingNodeOutputHandleY} ${this.halfwayX}, ${this.endingNodeInputHandleY} ${endingNode.inputHandleCoords}`;

    return `${startingPoint} ${curve}`;
  }
}

export class QuadraticPath {
  @tracked startingNode = null;
  @tracked endingNode = null;

  constructor(obj) {
    this.startingNode = obj.startingNode;
    this.endingNode = obj.endingNode;
  }

  // X position of halfway between
  // starting and ending nodes
  get halfwayX() {
    const startingNode = this.startingNode;
    const endingNode = this.endingNode;

    return (
      (endingNode.inputHandle.xPos - startingNode.outputHandle.xPos) / 2 +
      startingNode.outputHandle.xPos
    );
  }

  // Y position of the starting node output handle
  get startingNodeOutputHandleY() {
    return this.startingNode.outputHandle.yPos;
  }

  get firstLineCoords() {
    return `${this.halfwayX},${this.startingNodeOutputHandleY}`;
  }

  // Compute SVG commands
  get svgPath() {
    const startingNode = this.startingNode;
    const endingNode = this.endingNode;

    const startingPoint = `M ${startingNode.outputHandleCoords}`;
    const firstLine = `L ${this.firstLineCoords}`;

    if (this.startingNodeOutputHandleY === endingNode.inputHandle.yPos) {
      return `${startingPoint} L ${endingNode.inputHandle.xPos} ${endingNode.inputHandle.yPos}`;
    }

    let firstCurve, secondLine, secondCurve;

    if (this.startingNodeOutputHandleY < endingNode.inputHandle.yPos) {
      firstCurve = `Q ${this.halfwayX + 5},${this.startingNodeOutputHandleY} ${
        this.halfwayX + 5
      },${this.startingNodeOutputHandleY + 5}`;
      secondLine = `L ${this.halfwayX + 5},${endingNode.inputHandle.yPos - 5}`;
      secondCurve = `Q ${this.halfwayX + 5},${endingNode.inputHandle.yPos} ${
        this.halfwayX + 10
      },${endingNode.inputHandle.yPos}`;
    } else {
      firstCurve = `Q ${this.halfwayX + 5},${this.startingNodeOutputHandleY} ${
        this.halfwayX + 5
      },${this.startingNodeOutputHandleY - 5}`;
      secondLine = `L ${this.halfwayX + 5},${endingNode.inputHandle.yPos + 5}`;
      secondCurve = `Q ${this.halfwayX + 5},${endingNode.inputHandle.yPos} ${
        this.halfwayX + 10
      },${endingNode.inputHandle.yPos}`;
    }

    const thirdLine = `L ${endingNode.inputHandleCoords}`;

    return `${startingPoint} ${firstLine} ${firstCurve} ${secondLine} ${secondCurve} ${thirdLine}`;
  }
}
