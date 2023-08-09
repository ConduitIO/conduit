// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/proto/api/v1 (interfaces: ProcessorService_InspectProcessorInServer)

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	gomock "go.uber.org/mock/gomock"
	metadata "google.golang.org/grpc/metadata"
)

// ProcessorService_InspectProcessorInServer is a mock of ProcessorService_InspectProcessorInServer interface.
type ProcessorService_InspectProcessorInServer struct {
	ctrl     *gomock.Controller
	recorder *ProcessorService_InspectProcessorInServerMockRecorder
}

// ProcessorService_InspectProcessorInServerMockRecorder is the mock recorder for ProcessorService_InspectProcessorInServer.
type ProcessorService_InspectProcessorInServerMockRecorder struct {
	mock *ProcessorService_InspectProcessorInServer
}

// NewProcessorService_InspectProcessorInServer creates a new mock instance.
func NewProcessorService_InspectProcessorInServer(ctrl *gomock.Controller) *ProcessorService_InspectProcessorInServer {
	mock := &ProcessorService_InspectProcessorInServer{ctrl: ctrl}
	mock.recorder = &ProcessorService_InspectProcessorInServerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ProcessorService_InspectProcessorInServer) EXPECT() *ProcessorService_InspectProcessorInServerMockRecorder {
	return m.recorder
}

// Context mocks base method.
func (m *ProcessorService_InspectProcessorInServer) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).Context))
}

// RecvMsg mocks base method.
func (m *ProcessorService_InspectProcessorInServer) RecvMsg(arg0 interface{}) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) RecvMsg(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).RecvMsg), arg0)
}

// Send mocks base method.
func (m *ProcessorService_InspectProcessorInServer) Send(arg0 *apiv1.InspectProcessorInResponse) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) Send(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).Send), arg0)
}

// SendHeader mocks base method.
func (m *ProcessorService_InspectProcessorInServer) SendHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendHeader indicates an expected call of SendHeader.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) SendHeader(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendHeader", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).SendHeader), arg0)
}

// SendMsg mocks base method.
func (m *ProcessorService_InspectProcessorInServer) SendMsg(arg0 interface{}) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) SendMsg(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).SendMsg), arg0)
}

// SetHeader mocks base method.
func (m *ProcessorService_InspectProcessorInServer) SetHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SetHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SetHeader indicates an expected call of SetHeader.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) SetHeader(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetHeader", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).SetHeader), arg0)
}

// SetTrailer mocks base method.
func (m *ProcessorService_InspectProcessorInServer) SetTrailer(arg0 metadata.MD) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetTrailer", arg0)
}

// SetTrailer indicates an expected call of SetTrailer.
func (mr *ProcessorService_InspectProcessorInServerMockRecorder) SetTrailer(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetTrailer", reflect.TypeOf((*ProcessorService_InspectProcessorInServer)(nil).SetTrailer), arg0)
}
