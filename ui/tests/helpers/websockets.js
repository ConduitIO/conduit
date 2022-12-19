import EntityInspection from 'conduit-ui/utils/websockets/entity-inspection';

export function mockSocket() {
  class MockSocket extends EventTarget {
    triggerRecord() {
      const ev = new MessageEvent('message', {
        data: '{"result":{"position":"","operation":"","metadata":{},"key":{},"payload":{}}}',
      });
      this.dispatchEvent(ev);
    }

    triggerRecords(count) {
      for (let i = 0; i < count; i++) {
        this.triggerRecord();
      }
    }
  }

  return new MockSocket();
}

export function mockEntityInspection(id, socket = mockSocket()) {
  return new EntityInspection(id, socket);
}
