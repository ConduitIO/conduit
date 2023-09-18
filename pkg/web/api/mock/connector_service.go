// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/proto/api/v1 (interfaces: ConnectorService_InspectConnectorServer)
//
// Generated by this command:
//
//	mockgen -destination=mock/connector_service.go -package=mock -mock_names=ConnectorService_InspectConnectorServer=ConnectorService_InspectConnectorServer github.com/conduitio/conduit/proto/api/v1 ConnectorService_InspectConnectorServer
//
// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	gomock "go.uber.org/mock/gomock"
	metadata "google.golang.org/grpc/metadata"
)

// ConnectorService_InspectConnectorServer is a mock of ConnectorService_InspectConnectorServer interface.
type ConnectorService_InspectConnectorServer struct {
	ctrl     *gomock.Controller
	recorder *ConnectorService_InspectConnectorServerMockRecorder
}

// ConnectorService_InspectConnectorServerMockRecorder is the mock recorder for ConnectorService_InspectConnectorServer.
type ConnectorService_InspectConnectorServerMockRecorder struct {
	mock *ConnectorService_InspectConnectorServer
}

// NewConnectorService_InspectConnectorServer creates a new mock instance.
func NewConnectorService_InspectConnectorServer(ctrl *gomock.Controller) *ConnectorService_InspectConnectorServer {
	mock := &ConnectorService_InspectConnectorServer{ctrl: ctrl}
	mock.recorder = &ConnectorService_InspectConnectorServerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ConnectorService_InspectConnectorServer) EXPECT() *ConnectorService_InspectConnectorServerMockRecorder {
	return m.recorder
}

// Context mocks base method.
func (m *ConnectorService_InspectConnectorServer) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).Context))
}

// RecvMsg mocks base method.
func (m *ConnectorService_InspectConnectorServer) RecvMsg(arg0 any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) RecvMsg(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).RecvMsg), arg0)
}

// Send mocks base method.
func (m *ConnectorService_InspectConnectorServer) Send(arg0 *apiv1.InspectConnectorResponse) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) Send(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).Send), arg0)
}

// SendHeader mocks base method.
func (m *ConnectorService_InspectConnectorServer) SendHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendHeader indicates an expected call of SendHeader.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) SendHeader(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendHeader", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).SendHeader), arg0)
}

// SendMsg mocks base method.
func (m *ConnectorService_InspectConnectorServer) SendMsg(arg0 any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) SendMsg(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).SendMsg), arg0)
}

// SetHeader mocks base method.
func (m *ConnectorService_InspectConnectorServer) SetHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SetHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SetHeader indicates an expected call of SetHeader.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) SetHeader(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetHeader", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).SetHeader), arg0)
}

// SetTrailer mocks base method.
func (m *ConnectorService_InspectConnectorServer) SetTrailer(arg0 metadata.MD) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetTrailer", arg0)
}

// SetTrailer indicates an expected call of SetTrailer.
func (mr *ConnectorService_InspectConnectorServerMockRecorder) SetTrailer(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetTrailer", reflect.TypeOf((*ConnectorService_InspectConnectorServer)(nil).SetTrailer), arg0)
}
