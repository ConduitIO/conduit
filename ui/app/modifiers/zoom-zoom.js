import { modifier } from 'ember-modifier';
import { select } from 'd3-selection';
import { zoom } from 'd3-zoom';

export default modifier((element, [pipelineNodeManager]) => {
  function zoomed({ transform }) {
    const zoomStylePx = `translate(${transform.x}px,${transform.y}px) scale(${transform.k})`;
    const zoomStyle = `translate(${transform.x},${transform.y}) scale(${transform.k})`;
    select('#editor-bg').style('transform', zoomStylePx);
    select('#svg-g-container').attr('transform', zoomStyle);
    select('#editor-bg').style('transform-origin', '0 0');
  }

  const zoomSelection = select('#editor-container');
  const zoomObject = zoom().scaleExtent([0.5, 1.5]).on('zoom', zoomed);

  zoomSelection
    .call(zoomObject)
    .on('wheel.zoom', null)
    .on('dblclick.zoom', null);

  pipelineNodeManager.setZoomObject(zoomSelection, zoomObject);
  return () => {
    select('#editor-container').on('.zoom', null);
  };
});
