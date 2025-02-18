// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/proto/api/v1 (interfaces: ProcessorService_InspectProcessorOutServer)
//
// Generated by this command:
//
//	mockgen -typed -destination=mock/processor_service_out.go -package=mock -mock_names=ProcessorService_InspectProcessorOutServer=ProcessorService_InspectProcessorOutServer github.com/conduitio/conduit/proto/api/v1 ProcessorService_InspectProcessorOutServer
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
	isgomock struct{}
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
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) Context() *ProcessorService_InspectProcessorOutServerContextCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).Context))
	return &ProcessorService_InspectProcessorOutServerContextCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerContextCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerContextCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerContextCall) Return(arg0 context.Context) *ProcessorService_InspectProcessorOutServerContextCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerContextCall) Do(f func() context.Context) *ProcessorService_InspectProcessorOutServerContextCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerContextCall) DoAndReturn(f func() context.Context) *ProcessorService_InspectProcessorOutServerContextCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// RecvMsg mocks base method.
func (m_2 *ProcessorService_InspectProcessorOutServer) RecvMsg(m any) error {
	m_2.ctrl.T.Helper()
	ret := m_2.ctrl.Call(m_2, "RecvMsg", m)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) RecvMsg(m any) *ProcessorService_InspectProcessorOutServerRecvMsgCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).RecvMsg), m)
	return &ProcessorService_InspectProcessorOutServerRecvMsgCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerRecvMsgCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerRecvMsgCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerRecvMsgCall) Return(arg0 error) *ProcessorService_InspectProcessorOutServerRecvMsgCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerRecvMsgCall) Do(f func(any) error) *ProcessorService_InspectProcessorOutServerRecvMsgCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerRecvMsgCall) DoAndReturn(f func(any) error) *ProcessorService_InspectProcessorOutServerRecvMsgCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Send mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) Send(arg0 *apiv1.InspectProcessorOutResponse) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) Send(arg0 any) *ProcessorService_InspectProcessorOutServerSendCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).Send), arg0)
	return &ProcessorService_InspectProcessorOutServerSendCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerSendCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerSendCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerSendCall) Return(arg0 error) *ProcessorService_InspectProcessorOutServerSendCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerSendCall) Do(f func(*apiv1.InspectProcessorOutResponse) error) *ProcessorService_InspectProcessorOutServerSendCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerSendCall) DoAndReturn(f func(*apiv1.InspectProcessorOutResponse) error) *ProcessorService_InspectProcessorOutServerSendCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// SendHeader mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SendHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendHeader indicates an expected call of SendHeader.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SendHeader(arg0 any) *ProcessorService_InspectProcessorOutServerSendHeaderCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendHeader", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SendHeader), arg0)
	return &ProcessorService_InspectProcessorOutServerSendHeaderCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerSendHeaderCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerSendHeaderCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerSendHeaderCall) Return(arg0 error) *ProcessorService_InspectProcessorOutServerSendHeaderCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerSendHeaderCall) Do(f func(metadata.MD) error) *ProcessorService_InspectProcessorOutServerSendHeaderCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerSendHeaderCall) DoAndReturn(f func(metadata.MD) error) *ProcessorService_InspectProcessorOutServerSendHeaderCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// SendMsg mocks base method.
func (m_2 *ProcessorService_InspectProcessorOutServer) SendMsg(m any) error {
	m_2.ctrl.T.Helper()
	ret := m_2.ctrl.Call(m_2, "SendMsg", m)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SendMsg(m any) *ProcessorService_InspectProcessorOutServerSendMsgCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SendMsg), m)
	return &ProcessorService_InspectProcessorOutServerSendMsgCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerSendMsgCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerSendMsgCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerSendMsgCall) Return(arg0 error) *ProcessorService_InspectProcessorOutServerSendMsgCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerSendMsgCall) Do(f func(any) error) *ProcessorService_InspectProcessorOutServerSendMsgCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerSendMsgCall) DoAndReturn(f func(any) error) *ProcessorService_InspectProcessorOutServerSendMsgCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// SetHeader mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SetHeader(arg0 metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SetHeader", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// SetHeader indicates an expected call of SetHeader.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SetHeader(arg0 any) *ProcessorService_InspectProcessorOutServerSetHeaderCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetHeader", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SetHeader), arg0)
	return &ProcessorService_InspectProcessorOutServerSetHeaderCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerSetHeaderCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerSetHeaderCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerSetHeaderCall) Return(arg0 error) *ProcessorService_InspectProcessorOutServerSetHeaderCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerSetHeaderCall) Do(f func(metadata.MD) error) *ProcessorService_InspectProcessorOutServerSetHeaderCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerSetHeaderCall) DoAndReturn(f func(metadata.MD) error) *ProcessorService_InspectProcessorOutServerSetHeaderCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// SetTrailer mocks base method.
func (m *ProcessorService_InspectProcessorOutServer) SetTrailer(arg0 metadata.MD) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetTrailer", arg0)
}

// SetTrailer indicates an expected call of SetTrailer.
func (mr *ProcessorService_InspectProcessorOutServerMockRecorder) SetTrailer(arg0 any) *ProcessorService_InspectProcessorOutServerSetTrailerCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetTrailer", reflect.TypeOf((*ProcessorService_InspectProcessorOutServer)(nil).SetTrailer), arg0)
	return &ProcessorService_InspectProcessorOutServerSetTrailerCall{Call: call}
}

// ProcessorService_InspectProcessorOutServerSetTrailerCall wrap *gomock.Call
type ProcessorService_InspectProcessorOutServerSetTrailerCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorService_InspectProcessorOutServerSetTrailerCall) Return() *ProcessorService_InspectProcessorOutServerSetTrailerCall {
	c.Call = c.Call.Return()
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorService_InspectProcessorOutServerSetTrailerCall) Do(f func(metadata.MD)) *ProcessorService_InspectProcessorOutServerSetTrailerCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorService_InspectProcessorOutServerSetTrailerCall) DoAndReturn(f func(metadata.MD)) *ProcessorService_InspectProcessorOutServerSetTrailerCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}
