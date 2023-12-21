// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/web/api (interfaces: PluginOrchestrator)
//
// Generated by this command:
//
//	mockgen -destination=mock/plugin.go -package=mock -mock_names=PluginOrchestrator=PluginOrchestrator . PluginOrchestrator
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	plugin "github.com/conduitio/conduit/pkg/plugin"
	gomock "go.uber.org/mock/gomock"
)

// PluginOrchestrator is a mock of PluginOrchestrator interface.
type PluginOrchestrator struct {
	ctrl     *gomock.Controller
	recorder *PluginOrchestratorMockRecorder
}

// PluginOrchestratorMockRecorder is the mock recorder for PluginOrchestrator.
type PluginOrchestratorMockRecorder struct {
	mock *PluginOrchestrator
}

// NewPluginOrchestrator creates a new mock instance.
func NewPluginOrchestrator(ctrl *gomock.Controller) *PluginOrchestrator {
	mock := &PluginOrchestrator{ctrl: ctrl}
	mock.recorder = &PluginOrchestratorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *PluginOrchestrator) EXPECT() *PluginOrchestratorMockRecorder {
	return m.recorder
}

// List mocks base method.
func (m *PluginOrchestrator) List(arg0 context.Context) (map[string]plugin.Specification, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]plugin.Specification)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *PluginOrchestratorMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*PluginOrchestrator)(nil).List), arg0)
}
