// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/schemaregistry (interfaces: Service)
//
// Generated by this command:
//
//	mockgen -destination=mock/schema_service.go -package=mock -mock_names=Service=Service . Service
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
func (mr *ServiceMockRecorder) Check(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Check", reflect.TypeOf((*Service)(nil).Check), arg0)
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
func (mr *ServiceMockRecorder) Create(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*Service)(nil).Create), arg0, arg1, arg2)
}

// Get mocks base method.
func (m *Service) Get(arg0 context.Context, arg1 string) (schema.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1)
	ret0, _ := ret[0].(schema.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *ServiceMockRecorder) Get(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*Service)(nil).Get), arg0, arg1)
}
