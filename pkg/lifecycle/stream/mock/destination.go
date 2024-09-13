// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/lifecycle/stream (interfaces: Destination)
//
// Generated by this command:
//
//	mockgen -typed -destination=mock/destination.go -package=mock -mock_names=Destination=Destination . Destination
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	opencdc "github.com/conduitio/conduit-commons/opencdc"
	connector "github.com/conduitio/conduit/pkg/connector"
	gomock "go.uber.org/mock/gomock"
)

// Destination is a mock of Destination interface.
type Destination struct {
	ctrl     *gomock.Controller
	recorder *DestinationMockRecorder
}

// DestinationMockRecorder is the mock recorder for Destination.
type DestinationMockRecorder struct {
	mock *Destination
}

// NewDestination creates a new mock instance.
func NewDestination(ctrl *gomock.Controller) *Destination {
	mock := &Destination{ctrl: ctrl}
	mock.recorder = &DestinationMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *Destination) EXPECT() *DestinationMockRecorder {
	return m.recorder
}

// Ack mocks base method.
func (m *Destination) Ack(arg0 context.Context) ([]connector.DestinationAck, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Ack", arg0)
	ret0, _ := ret[0].([]connector.DestinationAck)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Ack indicates an expected call of Ack.
func (mr *DestinationMockRecorder) Ack(arg0 any) *DestinationAckCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Ack", reflect.TypeOf((*Destination)(nil).Ack), arg0)
	return &DestinationAckCall{Call: call}
}

// DestinationAckCall wrap *gomock.Call
type DestinationAckCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationAckCall) Return(arg0 []connector.DestinationAck, arg1 error) *DestinationAckCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationAckCall) Do(f func(context.Context) ([]connector.DestinationAck, error)) *DestinationAckCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationAckCall) DoAndReturn(f func(context.Context) ([]connector.DestinationAck, error)) *DestinationAckCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Errors mocks base method.
func (m *Destination) Errors() <-chan error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Errors")
	ret0, _ := ret[0].(<-chan error)
	return ret0
}

// Errors indicates an expected call of Errors.
func (mr *DestinationMockRecorder) Errors() *DestinationErrorsCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Errors", reflect.TypeOf((*Destination)(nil).Errors))
	return &DestinationErrorsCall{Call: call}
}

// DestinationErrorsCall wrap *gomock.Call
type DestinationErrorsCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationErrorsCall) Return(arg0 <-chan error) *DestinationErrorsCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationErrorsCall) Do(f func() <-chan error) *DestinationErrorsCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationErrorsCall) DoAndReturn(f func() <-chan error) *DestinationErrorsCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// ID mocks base method.
func (m *Destination) ID() string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ID")
	ret0, _ := ret[0].(string)
	return ret0
}

// ID indicates an expected call of ID.
func (mr *DestinationMockRecorder) ID() *DestinationIDCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ID", reflect.TypeOf((*Destination)(nil).ID))
	return &DestinationIDCall{Call: call}
}

// DestinationIDCall wrap *gomock.Call
type DestinationIDCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationIDCall) Return(arg0 string) *DestinationIDCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationIDCall) Do(f func() string) *DestinationIDCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationIDCall) DoAndReturn(f func() string) *DestinationIDCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Open mocks base method.
func (m *Destination) Open(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Open", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Open indicates an expected call of Open.
func (mr *DestinationMockRecorder) Open(arg0 any) *DestinationOpenCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Open", reflect.TypeOf((*Destination)(nil).Open), arg0)
	return &DestinationOpenCall{Call: call}
}

// DestinationOpenCall wrap *gomock.Call
type DestinationOpenCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationOpenCall) Return(arg0 error) *DestinationOpenCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationOpenCall) Do(f func(context.Context) error) *DestinationOpenCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationOpenCall) DoAndReturn(f func(context.Context) error) *DestinationOpenCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Stop mocks base method.
func (m *Destination) Stop(arg0 context.Context, arg1 opencdc.Position) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Stop", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Stop indicates an expected call of Stop.
func (mr *DestinationMockRecorder) Stop(arg0, arg1 any) *DestinationStopCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*Destination)(nil).Stop), arg0, arg1)
	return &DestinationStopCall{Call: call}
}

// DestinationStopCall wrap *gomock.Call
type DestinationStopCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationStopCall) Return(arg0 error) *DestinationStopCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationStopCall) Do(f func(context.Context, opencdc.Position) error) *DestinationStopCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationStopCall) DoAndReturn(f func(context.Context, opencdc.Position) error) *DestinationStopCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Teardown mocks base method.
func (m *Destination) Teardown(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Teardown", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Teardown indicates an expected call of Teardown.
func (mr *DestinationMockRecorder) Teardown(arg0 any) *DestinationTeardownCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Teardown", reflect.TypeOf((*Destination)(nil).Teardown), arg0)
	return &DestinationTeardownCall{Call: call}
}

// DestinationTeardownCall wrap *gomock.Call
type DestinationTeardownCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationTeardownCall) Return(arg0 error) *DestinationTeardownCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationTeardownCall) Do(f func(context.Context) error) *DestinationTeardownCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationTeardownCall) DoAndReturn(f func(context.Context) error) *DestinationTeardownCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Write mocks base method.
func (m *Destination) Write(arg0 context.Context, arg1 []opencdc.Record) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Write", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Write indicates an expected call of Write.
func (mr *DestinationMockRecorder) Write(arg0, arg1 any) *DestinationWriteCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Write", reflect.TypeOf((*Destination)(nil).Write), arg0, arg1)
	return &DestinationWriteCall{Call: call}
}

// DestinationWriteCall wrap *gomock.Call
type DestinationWriteCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *DestinationWriteCall) Return(arg0 error) *DestinationWriteCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *DestinationWriteCall) Do(f func(context.Context, []opencdc.Record) error) *DestinationWriteCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *DestinationWriteCall) DoAndReturn(f func(context.Context, []opencdc.Record) error) *DestinationWriteCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}