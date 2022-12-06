import Service from '@ember/service';
import EntityInspection from 'conduit-ui/utils/websockets/entity-inspection';

export default class WebsocketsService extends Service {
  _entities = new Map();

  connect(id, entityType) {
    if (!this._entities.get(id)) {
      const entitySocket = new WebSocket(
        `ws://localhost:8080/v1/${entityType}/${id}/inspect`
      );

      this._entities.set(id, new EntityInspection(id, entitySocket));
    }
  }

  disconnect(id) {
    const entity = this._entities.get(id);
    if (entity) {
      entity._socket.close();
      this._entities.delete(id);
    }
  }

  getEntityInspection(id) {
    return this._entities.get(id);
  }
}
