// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/processor (interfaces: Interface)

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	record "github.com/conduitio/conduit/pkg/record"
	gomock "github.com/golang/mock/gomock"
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

// Process mocks base method.
func (m *Processor) Process(arg0 context.Context, arg1 record.Record) (record.Record, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Process", arg0, arg1)
	ret0, _ := ret[0].(record.Record)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Process indicates an expected call of Process.
func (mr *ProcessorMockRecorder) Process(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Process", reflect.TypeOf((*Processor)(nil).Process), arg0, arg1)
}
