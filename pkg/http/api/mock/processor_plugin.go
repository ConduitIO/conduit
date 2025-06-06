// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/http/api (interfaces: ProcessorPluginOrchestrator)
//
// Generated by this command:
//
//	mockgen -typed -destination=mock/processor_plugin.go -package=mock -mock_names=ProcessorPluginOrchestrator=ProcessorPluginOrchestrator . ProcessorPluginOrchestrator
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	sdk "github.com/conduitio/conduit-processor-sdk"
	gomock "go.uber.org/mock/gomock"
)

// ProcessorPluginOrchestrator is a mock of ProcessorPluginOrchestrator interface.
type ProcessorPluginOrchestrator struct {
	ctrl     *gomock.Controller
	recorder *ProcessorPluginOrchestratorMockRecorder
	isgomock struct{}
}

// ProcessorPluginOrchestratorMockRecorder is the mock recorder for ProcessorPluginOrchestrator.
type ProcessorPluginOrchestratorMockRecorder struct {
	mock *ProcessorPluginOrchestrator
}

// NewProcessorPluginOrchestrator creates a new mock instance.
func NewProcessorPluginOrchestrator(ctrl *gomock.Controller) *ProcessorPluginOrchestrator {
	mock := &ProcessorPluginOrchestrator{ctrl: ctrl}
	mock.recorder = &ProcessorPluginOrchestratorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ProcessorPluginOrchestrator) EXPECT() *ProcessorPluginOrchestratorMockRecorder {
	return m.recorder
}

// List mocks base method.
func (m *ProcessorPluginOrchestrator) List(ctx context.Context) (map[string]sdk.Specification, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx)
	ret0, _ := ret[0].(map[string]sdk.Specification)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *ProcessorPluginOrchestratorMockRecorder) List(ctx any) *ProcessorPluginOrchestratorListCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*ProcessorPluginOrchestrator)(nil).List), ctx)
	return &ProcessorPluginOrchestratorListCall{Call: call}
}

// ProcessorPluginOrchestratorListCall wrap *gomock.Call
type ProcessorPluginOrchestratorListCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ProcessorPluginOrchestratorListCall) Return(arg0 map[string]sdk.Specification, arg1 error) *ProcessorPluginOrchestratorListCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ProcessorPluginOrchestratorListCall) Do(f func(context.Context) (map[string]sdk.Specification, error)) *ProcessorPluginOrchestratorListCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ProcessorPluginOrchestratorListCall) DoAndReturn(f func(context.Context) (map[string]sdk.Specification, error)) *ProcessorPluginOrchestratorListCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}
