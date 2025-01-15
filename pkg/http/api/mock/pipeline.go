// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/http/api (interfaces: PipelineOrchestrator)
//
// Generated by this command:
//
//	mockgen -typed -destination=mock/pipeline.go -package=mock -mock_names=PipelineOrchestrator=PipelineOrchestrator . PipelineOrchestrator
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	pipeline "github.com/conduitio/conduit/pkg/pipeline"
	gomock "go.uber.org/mock/gomock"
)

// PipelineOrchestrator is a mock of PipelineOrchestrator interface.
type PipelineOrchestrator struct {
	ctrl     *gomock.Controller
	recorder *PipelineOrchestratorMockRecorder
	isgomock struct{}
}

// PipelineOrchestratorMockRecorder is the mock recorder for PipelineOrchestrator.
type PipelineOrchestratorMockRecorder struct {
	mock *PipelineOrchestrator
}

// NewPipelineOrchestrator creates a new mock instance.
func NewPipelineOrchestrator(ctrl *gomock.Controller) *PipelineOrchestrator {
	mock := &PipelineOrchestrator{ctrl: ctrl}
	mock.recorder = &PipelineOrchestratorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *PipelineOrchestrator) EXPECT() *PipelineOrchestratorMockRecorder {
	return m.recorder
}

// Create mocks base method.
func (m *PipelineOrchestrator) Create(ctx context.Context, cfg pipeline.Config) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", ctx, cfg)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *PipelineOrchestratorMockRecorder) Create(ctx, cfg any) *PipelineOrchestratorCreateCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*PipelineOrchestrator)(nil).Create), ctx, cfg)
	return &PipelineOrchestratorCreateCall{Call: call}
}

// PipelineOrchestratorCreateCall wrap *gomock.Call
type PipelineOrchestratorCreateCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorCreateCall) Return(arg0 *pipeline.Instance, arg1 error) *PipelineOrchestratorCreateCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorCreateCall) Do(f func(context.Context, pipeline.Config) (*pipeline.Instance, error)) *PipelineOrchestratorCreateCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorCreateCall) DoAndReturn(f func(context.Context, pipeline.Config) (*pipeline.Instance, error)) *PipelineOrchestratorCreateCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Delete mocks base method.
func (m *PipelineOrchestrator) Delete(ctx context.Context, id string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", ctx, id)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *PipelineOrchestratorMockRecorder) Delete(ctx, id any) *PipelineOrchestratorDeleteCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*PipelineOrchestrator)(nil).Delete), ctx, id)
	return &PipelineOrchestratorDeleteCall{Call: call}
}

// PipelineOrchestratorDeleteCall wrap *gomock.Call
type PipelineOrchestratorDeleteCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorDeleteCall) Return(arg0 error) *PipelineOrchestratorDeleteCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorDeleteCall) Do(f func(context.Context, string) error) *PipelineOrchestratorDeleteCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorDeleteCall) DoAndReturn(f func(context.Context, string) error) *PipelineOrchestratorDeleteCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Get mocks base method.
func (m *PipelineOrchestrator) Get(ctx context.Context, id string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", ctx, id)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *PipelineOrchestratorMockRecorder) Get(ctx, id any) *PipelineOrchestratorGetCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*PipelineOrchestrator)(nil).Get), ctx, id)
	return &PipelineOrchestratorGetCall{Call: call}
}

// PipelineOrchestratorGetCall wrap *gomock.Call
type PipelineOrchestratorGetCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorGetCall) Return(arg0 *pipeline.Instance, arg1 error) *PipelineOrchestratorGetCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorGetCall) Do(f func(context.Context, string) (*pipeline.Instance, error)) *PipelineOrchestratorGetCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorGetCall) DoAndReturn(f func(context.Context, string) (*pipeline.Instance, error)) *PipelineOrchestratorGetCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// List mocks base method.
func (m *PipelineOrchestrator) List(ctx context.Context) map[string]*pipeline.Instance {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx)
	ret0, _ := ret[0].(map[string]*pipeline.Instance)
	return ret0
}

// List indicates an expected call of List.
func (mr *PipelineOrchestratorMockRecorder) List(ctx any) *PipelineOrchestratorListCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*PipelineOrchestrator)(nil).List), ctx)
	return &PipelineOrchestratorListCall{Call: call}
}

// PipelineOrchestratorListCall wrap *gomock.Call
type PipelineOrchestratorListCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorListCall) Return(arg0 map[string]*pipeline.Instance) *PipelineOrchestratorListCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorListCall) Do(f func(context.Context) map[string]*pipeline.Instance) *PipelineOrchestratorListCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorListCall) DoAndReturn(f func(context.Context) map[string]*pipeline.Instance) *PipelineOrchestratorListCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Start mocks base method.
func (m *PipelineOrchestrator) Start(ctx context.Context, id string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Start", ctx, id)
	ret0, _ := ret[0].(error)
	return ret0
}

// Start indicates an expected call of Start.
func (mr *PipelineOrchestratorMockRecorder) Start(ctx, id any) *PipelineOrchestratorStartCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Start", reflect.TypeOf((*PipelineOrchestrator)(nil).Start), ctx, id)
	return &PipelineOrchestratorStartCall{Call: call}
}

// PipelineOrchestratorStartCall wrap *gomock.Call
type PipelineOrchestratorStartCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorStartCall) Return(arg0 error) *PipelineOrchestratorStartCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorStartCall) Do(f func(context.Context, string) error) *PipelineOrchestratorStartCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorStartCall) DoAndReturn(f func(context.Context, string) error) *PipelineOrchestratorStartCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Stop mocks base method.
func (m *PipelineOrchestrator) Stop(ctx context.Context, id string, force bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Stop", ctx, id, force)
	ret0, _ := ret[0].(error)
	return ret0
}

// Stop indicates an expected call of Stop.
func (mr *PipelineOrchestratorMockRecorder) Stop(ctx, id, force any) *PipelineOrchestratorStopCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*PipelineOrchestrator)(nil).Stop), ctx, id, force)
	return &PipelineOrchestratorStopCall{Call: call}
}

// PipelineOrchestratorStopCall wrap *gomock.Call
type PipelineOrchestratorStopCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorStopCall) Return(arg0 error) *PipelineOrchestratorStopCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorStopCall) Do(f func(context.Context, string, bool) error) *PipelineOrchestratorStopCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorStopCall) DoAndReturn(f func(context.Context, string, bool) error) *PipelineOrchestratorStopCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Update mocks base method.
func (m *PipelineOrchestrator) Update(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", ctx, id, cfg)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *PipelineOrchestratorMockRecorder) Update(ctx, id, cfg any) *PipelineOrchestratorUpdateCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*PipelineOrchestrator)(nil).Update), ctx, id, cfg)
	return &PipelineOrchestratorUpdateCall{Call: call}
}

// PipelineOrchestratorUpdateCall wrap *gomock.Call
type PipelineOrchestratorUpdateCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorUpdateCall) Return(arg0 *pipeline.Instance, arg1 error) *PipelineOrchestratorUpdateCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorUpdateCall) Do(f func(context.Context, string, pipeline.Config) (*pipeline.Instance, error)) *PipelineOrchestratorUpdateCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorUpdateCall) DoAndReturn(f func(context.Context, string, pipeline.Config) (*pipeline.Instance, error)) *PipelineOrchestratorUpdateCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// UpdateDLQ mocks base method.
func (m *PipelineOrchestrator) UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateDLQ", ctx, id, dlq)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// UpdateDLQ indicates an expected call of UpdateDLQ.
func (mr *PipelineOrchestratorMockRecorder) UpdateDLQ(ctx, id, dlq any) *PipelineOrchestratorUpdateDLQCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateDLQ", reflect.TypeOf((*PipelineOrchestrator)(nil).UpdateDLQ), ctx, id, dlq)
	return &PipelineOrchestratorUpdateDLQCall{Call: call}
}

// PipelineOrchestratorUpdateDLQCall wrap *gomock.Call
type PipelineOrchestratorUpdateDLQCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *PipelineOrchestratorUpdateDLQCall) Return(arg0 *pipeline.Instance, arg1 error) *PipelineOrchestratorUpdateDLQCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *PipelineOrchestratorUpdateDLQCall) Do(f func(context.Context, string, pipeline.DLQ) (*pipeline.Instance, error)) *PipelineOrchestratorUpdateDLQCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *PipelineOrchestratorUpdateDLQCall) DoAndReturn(f func(context.Context, string, pipeline.DLQ) (*pipeline.Instance, error)) *PipelineOrchestratorUpdateDLQCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}
