// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/processor (interfaces: Interface)
//
// Generated by this command:
//
//	mockgen -destination=mock/processor.go -package=mock -mock_names=Interface=Processor . Interface
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	inspector "github.com/conduitio/conduit/pkg/inspector"
	record "github.com/conduitio/conduit/pkg/record"
	gomock "go.uber.org/mock/gomock"
)

// Processor is a mock of Interface interface.
type Processor struct {
	ctrl     *gomock.Controller
	recorder *ProcessorMockRecorder
}

// ProcessorMockRecorder is the mock recorder for Processor.
type ProcessorMockRecorder struct {
	mock *Processor
}

// NewProcessor creates a new mock instance.
func NewProcessor(ctrl *gomock.Controller) *Processor {
	mock := &Processor{ctrl: ctrl}
	mock.recorder = &ProcessorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *Processor) EXPECT() *ProcessorMockRecorder {
	return m.recorder
}

// Close mocks base method.
func (m *Processor) Close() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Close")
}

// Close indicates an expected call of Close.
func (mr *ProcessorMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*Processor)(nil).Close))
}

// InspectIn mocks base method.
func (m *Processor) InspectIn(arg0 context.Context, arg1 string) *inspector.Session {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "InspectIn", arg0, arg1)
	ret0, _ := ret[0].(*inspector.Session)
	return ret0
}

// InspectIn indicates an expected call of InspectIn.
func (mr *ProcessorMockRecorder) InspectIn(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "InspectIn", reflect.TypeOf((*Processor)(nil).InspectIn), arg0, arg1)
}

// InspectOut mocks base method.
func (m *Processor) InspectOut(arg0 context.Context, arg1 string) *inspector.Session {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "InspectOut", arg0, arg1)
	ret0, _ := ret[0].(*inspector.Session)
	return ret0
}

// InspectOut indicates an expected call of InspectOut.
func (mr *ProcessorMockRecorder) InspectOut(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "InspectOut", reflect.TypeOf((*Processor)(nil).InspectOut), arg0, arg1)
}

// Process mocks base method.
func (m *Processor) Process(arg0 context.Context, arg1 record.Record) (record.Record, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Process", arg0, arg1)
	ret0, _ := ret[0].(record.Record)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Process indicates an expected call of Process.
func (mr *ProcessorMockRecorder) Process(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Process", reflect.TypeOf((*Processor)(nil).Process), arg0, arg1)
}
