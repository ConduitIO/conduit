// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/schemaregistry (interfaces: Service)
//
// Generated by this command:
//
//	mockgen -typed -destination=mock/schema_service.go -package=mock -mock_names=Service=Service . Service
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	schema "github.com/conduitio/conduit-commons/schema"
	gomock "go.uber.org/mock/gomock"
)

// Service is a mock of Service interface.
type Service struct {
	ctrl     *gomock.Controller
	recorder *ServiceMockRecorder
}

// ServiceMockRecorder is the mock recorder for Service.
type ServiceMockRecorder struct {
	mock *Service
}

// NewService creates a new mock instance.
func NewService(ctrl *gomock.Controller) *Service {
	mock := &Service{ctrl: ctrl}
	mock.recorder = &ServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *Service) EXPECT() *ServiceMockRecorder {
	return m.recorder
}

// Check mocks base method.
func (m *Service) Check(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Check", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Check indicates an expected call of Check.
func (mr *ServiceMockRecorder) Check(arg0 any) *ServiceCheckCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Check", reflect.TypeOf((*Service)(nil).Check), arg0)
	return &ServiceCheckCall{Call: call}
}

// ServiceCheckCall wrap *gomock.Call
type ServiceCheckCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ServiceCheckCall) Return(arg0 error) *ServiceCheckCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ServiceCheckCall) Do(f func(context.Context) error) *ServiceCheckCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ServiceCheckCall) DoAndReturn(f func(context.Context) error) *ServiceCheckCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Create mocks base method.
func (m *Service) Create(arg0 context.Context, arg1 string, arg2 []byte) (schema.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", arg0, arg1, arg2)
	ret0, _ := ret[0].(schema.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *ServiceMockRecorder) Create(arg0, arg1, arg2 any) *ServiceCreateCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*Service)(nil).Create), arg0, arg1, arg2)
	return &ServiceCreateCall{Call: call}
}

// ServiceCreateCall wrap *gomock.Call
type ServiceCreateCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ServiceCreateCall) Return(arg0 schema.Instance, arg1 error) *ServiceCreateCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ServiceCreateCall) Do(f func(context.Context, string, []byte) (schema.Instance, error)) *ServiceCreateCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ServiceCreateCall) DoAndReturn(f func(context.Context, string, []byte) (schema.Instance, error)) *ServiceCreateCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Get mocks base method.
func (m *Service) Get(arg0 context.Context, arg1 string, arg2 int) (schema.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1, arg2)
	ret0, _ := ret[0].(schema.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *ServiceMockRecorder) Get(arg0, arg1, arg2 any) *ServiceGetCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*Service)(nil).Get), arg0, arg1, arg2)
	return &ServiceGetCall{Call: call}
}

// ServiceGetCall wrap *gomock.Call
type ServiceGetCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *ServiceGetCall) Return(arg0 schema.Instance, arg1 error) *ServiceGetCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *ServiceGetCall) Do(f func(context.Context, string, int) (schema.Instance, error)) *ServiceGetCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *ServiceGetCall) DoAndReturn(f func(context.Context, string, int) (schema.Instance, error)) *ServiceGetCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}
