// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/plugin (interfaces: Dispenser,DestinationPlugin,SourcePlugin,SpecifierPlugin)

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	plugin "github.com/conduitio/conduit/pkg/plugin"
	record "github.com/conduitio/conduit/pkg/record"
	gomock "go.uber.org/mock/gomock"
)

// Dispenser is a mock of Dispenser interface.
type Dispenser struct {
	ctrl     *gomock.Controller
	recorder *DispenserMockRecorder
}

// DispenserMockRecorder is the mock recorder for Dispenser.
type DispenserMockRecorder struct {
	mock *Dispenser
}

// NewDispenser creates a new mock instance.
func NewDispenser(ctrl *gomock.Controller) *Dispenser {
	mock := &Dispenser{ctrl: ctrl}
	mock.recorder = &DispenserMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *Dispenser) EXPECT() *DispenserMockRecorder {
	return m.recorder
}

// DispenseDestination mocks base method.
func (m *Dispenser) DispenseDestination() (plugin.DestinationPlugin, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DispenseDestination")
	ret0, _ := ret[0].(plugin.DestinationPlugin)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// DispenseDestination indicates an expected call of DispenseDestination.
func (mr *DispenserMockRecorder) DispenseDestination() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DispenseDestination", reflect.TypeOf((*Dispenser)(nil).DispenseDestination))
}

// DispenseSource mocks base method.
func (m *Dispenser) DispenseSource() (plugin.SourcePlugin, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DispenseSource")
	ret0, _ := ret[0].(plugin.SourcePlugin)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// DispenseSource indicates an expected call of DispenseSource.
func (mr *DispenserMockRecorder) DispenseSource() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DispenseSource", reflect.TypeOf((*Dispenser)(nil).DispenseSource))
}

// DispenseSpecifier mocks base method.
func (m *Dispenser) DispenseSpecifier() (plugin.SpecifierPlugin, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DispenseSpecifier")
	ret0, _ := ret[0].(plugin.SpecifierPlugin)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// DispenseSpecifier indicates an expected call of DispenseSpecifier.
func (mr *DispenserMockRecorder) DispenseSpecifier() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DispenseSpecifier", reflect.TypeOf((*Dispenser)(nil).DispenseSpecifier))
}

// DestinationPlugin is a mock of DestinationPlugin interface.
type DestinationPlugin struct {
	ctrl     *gomock.Controller
	recorder *DestinationPluginMockRecorder
}

// DestinationPluginMockRecorder is the mock recorder for DestinationPlugin.
type DestinationPluginMockRecorder struct {
	mock *DestinationPlugin
}

// NewDestinationPlugin creates a new mock instance.
func NewDestinationPlugin(ctrl *gomock.Controller) *DestinationPlugin {
	mock := &DestinationPlugin{ctrl: ctrl}
	mock.recorder = &DestinationPluginMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *DestinationPlugin) EXPECT() *DestinationPluginMockRecorder {
	return m.recorder
}

// Ack mocks base method.
func (m *DestinationPlugin) Ack(arg0 context.Context) (record.Position, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Ack", arg0)
	ret0, _ := ret[0].(record.Position)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Ack indicates an expected call of Ack.
func (mr *DestinationPluginMockRecorder) Ack(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Ack", reflect.TypeOf((*DestinationPlugin)(nil).Ack), arg0)
}

// Configure mocks base method.
func (m *DestinationPlugin) Configure(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Configure", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Configure indicates an expected call of Configure.
func (mr *DestinationPluginMockRecorder) Configure(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Configure", reflect.TypeOf((*DestinationPlugin)(nil).Configure), arg0, arg1)
}

// LifecycleOnCreated mocks base method.
func (m *DestinationPlugin) LifecycleOnCreated(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnCreated", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnCreated indicates an expected call of LifecycleOnCreated.
func (mr *DestinationPluginMockRecorder) LifecycleOnCreated(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnCreated", reflect.TypeOf((*DestinationPlugin)(nil).LifecycleOnCreated), arg0, arg1)
}

// LifecycleOnDeleted mocks base method.
func (m *DestinationPlugin) LifecycleOnDeleted(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnDeleted", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnDeleted indicates an expected call of LifecycleOnDeleted.
func (mr *DestinationPluginMockRecorder) LifecycleOnDeleted(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnDeleted", reflect.TypeOf((*DestinationPlugin)(nil).LifecycleOnDeleted), arg0, arg1)
}

// LifecycleOnUpdated mocks base method.
func (m *DestinationPlugin) LifecycleOnUpdated(arg0 context.Context, arg1, arg2 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnUpdated", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnUpdated indicates an expected call of LifecycleOnUpdated.
func (mr *DestinationPluginMockRecorder) LifecycleOnUpdated(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnUpdated", reflect.TypeOf((*DestinationPlugin)(nil).LifecycleOnUpdated), arg0, arg1, arg2)
}

// Start mocks base method.
func (m *DestinationPlugin) Start(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Start", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Start indicates an expected call of Start.
func (mr *DestinationPluginMockRecorder) Start(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Start", reflect.TypeOf((*DestinationPlugin)(nil).Start), arg0)
}

// Stop mocks base method.
func (m *DestinationPlugin) Stop(arg0 context.Context, arg1 record.Position) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Stop", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Stop indicates an expected call of Stop.
func (mr *DestinationPluginMockRecorder) Stop(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*DestinationPlugin)(nil).Stop), arg0, arg1)
}

// Teardown mocks base method.
func (m *DestinationPlugin) Teardown(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Teardown", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Teardown indicates an expected call of Teardown.
func (mr *DestinationPluginMockRecorder) Teardown(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Teardown", reflect.TypeOf((*DestinationPlugin)(nil).Teardown), arg0)
}

// Write mocks base method.
func (m *DestinationPlugin) Write(arg0 context.Context, arg1 record.Record) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Write", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Write indicates an expected call of Write.
func (mr *DestinationPluginMockRecorder) Write(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Write", reflect.TypeOf((*DestinationPlugin)(nil).Write), arg0, arg1)
}

// SourcePlugin is a mock of SourcePlugin interface.
type SourcePlugin struct {
	ctrl     *gomock.Controller
	recorder *SourcePluginMockRecorder
}

// SourcePluginMockRecorder is the mock recorder for SourcePlugin.
type SourcePluginMockRecorder struct {
	mock *SourcePlugin
}

// NewSourcePlugin creates a new mock instance.
func NewSourcePlugin(ctrl *gomock.Controller) *SourcePlugin {
	mock := &SourcePlugin{ctrl: ctrl}
	mock.recorder = &SourcePluginMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *SourcePlugin) EXPECT() *SourcePluginMockRecorder {
	return m.recorder
}

// Ack mocks base method.
func (m *SourcePlugin) Ack(arg0 context.Context, arg1 record.Position) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Ack", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Ack indicates an expected call of Ack.
func (mr *SourcePluginMockRecorder) Ack(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Ack", reflect.TypeOf((*SourcePlugin)(nil).Ack), arg0, arg1)
}

// Configure mocks base method.
func (m *SourcePlugin) Configure(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Configure", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Configure indicates an expected call of Configure.
func (mr *SourcePluginMockRecorder) Configure(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Configure", reflect.TypeOf((*SourcePlugin)(nil).Configure), arg0, arg1)
}

// LifecycleOnCreated mocks base method.
func (m *SourcePlugin) LifecycleOnCreated(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnCreated", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnCreated indicates an expected call of LifecycleOnCreated.
func (mr *SourcePluginMockRecorder) LifecycleOnCreated(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnCreated", reflect.TypeOf((*SourcePlugin)(nil).LifecycleOnCreated), arg0, arg1)
}

// LifecycleOnDeleted mocks base method.
func (m *SourcePlugin) LifecycleOnDeleted(arg0 context.Context, arg1 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnDeleted", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnDeleted indicates an expected call of LifecycleOnDeleted.
func (mr *SourcePluginMockRecorder) LifecycleOnDeleted(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnDeleted", reflect.TypeOf((*SourcePlugin)(nil).LifecycleOnDeleted), arg0, arg1)
}

// LifecycleOnUpdated mocks base method.
func (m *SourcePlugin) LifecycleOnUpdated(arg0 context.Context, arg1, arg2 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifecycleOnUpdated", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// LifecycleOnUpdated indicates an expected call of LifecycleOnUpdated.
func (mr *SourcePluginMockRecorder) LifecycleOnUpdated(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifecycleOnUpdated", reflect.TypeOf((*SourcePlugin)(nil).LifecycleOnUpdated), arg0, arg1, arg2)
}

// Read mocks base method.
func (m *SourcePlugin) Read(arg0 context.Context) (record.Record, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Read", arg0)
	ret0, _ := ret[0].(record.Record)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Read indicates an expected call of Read.
func (mr *SourcePluginMockRecorder) Read(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Read", reflect.TypeOf((*SourcePlugin)(nil).Read), arg0)
}

// Start mocks base method.
func (m *SourcePlugin) Start(arg0 context.Context, arg1 record.Position) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Start", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Start indicates an expected call of Start.
func (mr *SourcePluginMockRecorder) Start(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Start", reflect.TypeOf((*SourcePlugin)(nil).Start), arg0, arg1)
}

// Stop mocks base method.
func (m *SourcePlugin) Stop(arg0 context.Context) (record.Position, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Stop", arg0)
	ret0, _ := ret[0].(record.Position)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Stop indicates an expected call of Stop.
func (mr *SourcePluginMockRecorder) Stop(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*SourcePlugin)(nil).Stop), arg0)
}

// Teardown mocks base method.
func (m *SourcePlugin) Teardown(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Teardown", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Teardown indicates an expected call of Teardown.
func (mr *SourcePluginMockRecorder) Teardown(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Teardown", reflect.TypeOf((*SourcePlugin)(nil).Teardown), arg0)
}

// SpecifierPlugin is a mock of SpecifierPlugin interface.
type SpecifierPlugin struct {
	ctrl     *gomock.Controller
	recorder *SpecifierPluginMockRecorder
}

// SpecifierPluginMockRecorder is the mock recorder for SpecifierPlugin.
type SpecifierPluginMockRecorder struct {
	mock *SpecifierPlugin
}

// NewSpecifierPlugin creates a new mock instance.
func NewSpecifierPlugin(ctrl *gomock.Controller) *SpecifierPlugin {
	mock := &SpecifierPlugin{ctrl: ctrl}
	mock.recorder = &SpecifierPluginMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *SpecifierPlugin) EXPECT() *SpecifierPluginMockRecorder {
	return m.recorder
}

// Specify mocks base method.
func (m *SpecifierPlugin) Specify() (plugin.Specification, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Specify")
	ret0, _ := ret[0].(plugin.Specification)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Specify indicates an expected call of Specify.
func (mr *SpecifierPluginMockRecorder) Specify() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Specify", reflect.TypeOf((*SpecifierPlugin)(nil).Specify))
}
