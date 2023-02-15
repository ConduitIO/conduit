// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/foundation/database (interfaces: DB)

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	database "github.com/conduitio/conduit/pkg/foundation/database"
	gomock "github.com/golang/mock/gomock"
)

// DB is a mock of DB interface.
type DB struct {
	ctrl     *gomock.Controller
	recorder *DBMockRecorder
}

// DBMockRecorder is the mock recorder for DB.
type DBMockRecorder struct {
	mock *DB
}

// NewDB creates a new mock instance.
func NewDB(ctrl *gomock.Controller) *DB {
	mock := &DB{ctrl: ctrl}
	mock.recorder = &DBMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *DB) EXPECT() *DBMockRecorder {
	return m.recorder
}

// Close mocks base method.
func (m *DB) Close() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

// Close indicates an expected call of Close.
func (mr *DBMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*DB)(nil).Close))
}

// Get mocks base method.
func (m *DB) Get(arg0 context.Context, arg1 string) ([]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1)
	ret0, _ := ret[0].([]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *DBMockRecorder) Get(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*DB)(nil).Get), arg0, arg1)
}

// GetKeys mocks base method.
func (m *DB) GetKeys(arg0 context.Context, arg1 string) ([]string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetKeys", arg0, arg1)
	ret0, _ := ret[0].([]string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetKeys indicates an expected call of GetKeys.
func (mr *DBMockRecorder) GetKeys(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetKeys", reflect.TypeOf((*DB)(nil).GetKeys), arg0, arg1)
}

// NewTransaction mocks base method.
func (m *DB) NewTransaction(arg0 context.Context, arg1 bool) (database.Transaction, context.Context, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewTransaction", arg0, arg1)
	ret0, _ := ret[0].(database.Transaction)
	ret1, _ := ret[1].(context.Context)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

// NewTransaction indicates an expected call of NewTransaction.
func (mr *DBMockRecorder) NewTransaction(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewTransaction", reflect.TypeOf((*DB)(nil).NewTransaction), arg0, arg1)
}

// Ping mocks base method.
func (m *DB) Ping(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Ping", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Ping indicates an expected call of Ping.
func (mr *DBMockRecorder) Ping(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Ping", reflect.TypeOf((*DB)(nil).Ping), arg0)
}

// Set mocks base method.
func (m *DB) Set(arg0 context.Context, arg1 string, arg2 []byte) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Set", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// Set indicates an expected call of Set.
func (mr *DBMockRecorder) Set(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Set", reflect.TypeOf((*DB)(nil).Set), arg0, arg1, arg2)
}
