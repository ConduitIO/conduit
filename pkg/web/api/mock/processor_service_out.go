// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/proto/api/v1 (interfaces: ProcessorService_InspectProcessorOutServer)
//
// Generated by this command:
//
//	mockgen -destination=mock/processor_service_out.go -package=mock -mock_names=ProcessorService_InspectProcessorOutServer=ProcessorService_InspectProcessorOutServer github.com/conduitio/conduit/proto/api/v1 ProcessorService_InspectProcessorOutServer
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

// ProcessorService_InspectProcessorOutServer is a mock of ProcessorService_InspectProcessorOutServer interface.
type ProcessorService_InspectProcessorOutServer struct {
	ctrl     *gomock.Controller
	recorder *ProcessorService_InspectProcessorOutServerMockRecorder
}

// ProcessorService_InspectProcessorOutServerMockRecorder is the mock recorder for ProcessorService_InspectProcessorOutServer.
type ProcessorService_InspectProcessorOutServerMockRecorder struct {
	mock *ProcessorService_InspectProcessorOutServer
}

// NewProcessorService_InspectProcessorOutServer creates a new mock instance.
func NewProcessorService_InspectProcessorOutServer(ctrl *gomock.Controller) *ProcessorService_InspectProcessorOutServer {
	mock := &ProcessorService_InspectProcessorOutServer{ctrl: ctrl}
	mock.recorder = &ProcessorService_InspectProcessorOutServerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ProcessorService_InspectProcessorOutServer) EXPECT() *ProcessorService_InspectProcessorOutServerMockRecorder {
	return m.recorder
}

// Context mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).Context))
}

// RecvMsg mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) RecvMsg(arg0 any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) RecvMsg(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).RecvMsg), arg0)
}

// Send mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) Send(arg0 *apiv1.InspectProcessorOutResponse) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) Send(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).Send), arg0)
}

// SendHeader mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SendHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendHeader indicates an expected call of SendHeader.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SendHeader(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendHeader", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SendHeader), arg0)
}

// SendMsg mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SendMsg(arg0 any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SendMsg(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SendMsg), arg0)
}

// SetHeader mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SetHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SetHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SetHeader indicates an expected call of SetHeader.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SetHeader(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetHeader", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SetHeader), arg0)
}

// SetTrailer mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SetTrailer(arg0 metadata.MD) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetTrailer", arg0)
}

// SetTrailer indicates an expected call of SetTrailer.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SetTrailer(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetTrailer", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SetTrailer), arg0)
}
