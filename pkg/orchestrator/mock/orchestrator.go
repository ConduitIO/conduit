// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/conduitio/conduit/pkg/orchestrator (interfaces: PipelineService,ConnectorService,ProcessorService,ConnectorPluginService,ProcessorPluginService)
//
// Generated by this command:
//
//	mockgen -destination=mock/orchestrator.go -package=mock -mock_names=PipelineService=PipelineService,ConnectorService=ConnectorService,ProcessorService=ProcessorService,ConnectorPluginService=ConnectorPluginService,ProcessorPluginService=ProcessorPluginService . PipelineService,ConnectorService,ProcessorService,ConnectorPluginService,ProcessorPluginService
//

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	sdk "github.com/conduitio/conduit-processor-sdk"
	connector "github.com/conduitio/conduit/pkg/connector"
	log "github.com/conduitio/conduit/pkg/foundation/log"
	pipeline "github.com/conduitio/conduit/pkg/pipeline"
	connector0 "github.com/conduitio/conduit/pkg/plugin/connector"
	processor "github.com/conduitio/conduit/pkg/processor"
	gomock "go.uber.org/mock/gomock"
)

// PipelineService is a mock of PipelineService interface.
type PipelineService struct {
	ctrl     *gomock.Controller
	recorder *PipelineServiceMockRecorder
}

// PipelineServiceMockRecorder is the mock recorder for PipelineService.
type PipelineServiceMockRecorder struct {
	mock *PipelineService
}

// NewPipelineService creates a new mock instance.
func NewPipelineService(ctrl *gomock.Controller) *PipelineService {
	mock := &PipelineService{ctrl: ctrl}
	mock.recorder = &PipelineServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *PipelineService) EXPECT() *PipelineServiceMockRecorder {
	return m.recorder
}

// AddConnector mocks base method.
func (m *PipelineService) AddConnector(arg0 context.Context, arg1, arg2 string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddConnector", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// AddConnector indicates an expected call of AddConnector.
func (mr *PipelineServiceMockRecorder) AddConnector(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddConnector", reflect.TypeOf((*PipelineService)(nil).AddConnector), arg0, arg1, arg2)
}

// AddProcessor mocks base method.
func (m *PipelineService) AddProcessor(arg0 context.Context, arg1, arg2 string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddProcessor", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// AddProcessor indicates an expected call of AddProcessor.
func (mr *PipelineServiceMockRecorder) AddProcessor(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddProcessor", reflect.TypeOf((*PipelineService)(nil).AddProcessor), arg0, arg1, arg2)
}

// Create mocks base method.
func (m *PipelineService) Create(arg0 context.Context, arg1 string, arg2 pipeline.Config, arg3 pipeline.ProvisionType) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", arg0, arg1, arg2, arg3)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *PipelineServiceMockRecorder) Create(arg0, arg1, arg2, arg3 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*PipelineService)(nil).Create), arg0, arg1, arg2, arg3)
}

// Delete mocks base method.
func (m *PipelineService) Delete(arg0 context.Context, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *PipelineServiceMockRecorder) Delete(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*PipelineService)(nil).Delete), arg0, arg1)
}

// Get mocks base method.
func (m *PipelineService) Get(arg0 context.Context, arg1 string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *PipelineServiceMockRecorder) Get(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*PipelineService)(nil).Get), arg0, arg1)
}

// List mocks base method.
func (m *PipelineService) List(arg0 context.Context) map[string]*pipeline.Instance {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]*pipeline.Instance)
	return ret0
}

// List indicates an expected call of List.
func (mr *PipelineServiceMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*PipelineService)(nil).List), arg0)
}

// RemoveConnector mocks base method.
func (m *PipelineService) RemoveConnector(arg0 context.Context, arg1, arg2 string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveConnector", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// RemoveConnector indicates an expected call of RemoveConnector.
func (mr *PipelineServiceMockRecorder) RemoveConnector(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveConnector", reflect.TypeOf((*PipelineService)(nil).RemoveConnector), arg0, arg1, arg2)
}

// RemoveProcessor mocks base method.
func (m *PipelineService) RemoveProcessor(arg0 context.Context, arg1, arg2 string) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveProcessor", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// RemoveProcessor indicates an expected call of RemoveProcessor.
func (mr *PipelineServiceMockRecorder) RemoveProcessor(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveProcessor", reflect.TypeOf((*PipelineService)(nil).RemoveProcessor), arg0, arg1, arg2)
}

// Start mocks base method.
func (m *PipelineService) Start(arg0 context.Context, arg1 pipeline.ConnectorFetcher, arg2 pipeline.ProcessorService, arg3 pipeline.PluginDispenserFetcher, arg4 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Start", arg0, arg1, arg2, arg3, arg4)
	ret0, _ := ret[0].(error)
	return ret0
}

// Start indicates an expected call of Start.
func (mr *PipelineServiceMockRecorder) Start(arg0, arg1, arg2, arg3, arg4 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Start", reflect.TypeOf((*PipelineService)(nil).Start), arg0, arg1, arg2, arg3, arg4)
}

// Stop mocks base method.
func (m *PipelineService) Stop(arg0 context.Context, arg1 string, arg2 bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Stop", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// Stop indicates an expected call of Stop.
func (mr *PipelineServiceMockRecorder) Stop(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*PipelineService)(nil).Stop), arg0, arg1, arg2)
}

// Update mocks base method.
func (m *PipelineService) Update(arg0 context.Context, arg1 string, arg2 pipeline.Config) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *PipelineServiceMockRecorder) Update(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*PipelineService)(nil).Update), arg0, arg1, arg2)
}

// UpdateDLQ mocks base method.
func (m *PipelineService) UpdateDLQ(arg0 context.Context, arg1 string, arg2 pipeline.DLQ) (*pipeline.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateDLQ", arg0, arg1, arg2)
	ret0, _ := ret[0].(*pipeline.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// UpdateDLQ indicates an expected call of UpdateDLQ.
func (mr *PipelineServiceMockRecorder) UpdateDLQ(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateDLQ", reflect.TypeOf((*PipelineService)(nil).UpdateDLQ), arg0, arg1, arg2)
}

// ConnectorService is a mock of ConnectorService interface.
type ConnectorService struct {
	ctrl     *gomock.Controller
	recorder *ConnectorServiceMockRecorder
}

// ConnectorServiceMockRecorder is the mock recorder for ConnectorService.
type ConnectorServiceMockRecorder struct {
	mock *ConnectorService
}

// NewConnectorService creates a new mock instance.
func NewConnectorService(ctrl *gomock.Controller) *ConnectorService {
	mock := &ConnectorService{ctrl: ctrl}
	mock.recorder = &ConnectorServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ConnectorService) EXPECT() *ConnectorServiceMockRecorder {
	return m.recorder
}

// AddProcessor mocks base method.
func (m *ConnectorService) AddProcessor(arg0 context.Context, arg1, arg2 string) (*connector.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddProcessor", arg0, arg1, arg2)
	ret0, _ := ret[0].(*connector.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// AddProcessor indicates an expected call of AddProcessor.
func (mr *ConnectorServiceMockRecorder) AddProcessor(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddProcessor", reflect.TypeOf((*ConnectorService)(nil).AddProcessor), arg0, arg1, arg2)
}

// Create mocks base method.
func (m *ConnectorService) Create(arg0 context.Context, arg1 string, arg2 connector.Type, arg3, arg4 string, arg5 connector.Config, arg6 connector.ProvisionType) (*connector.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", arg0, arg1, arg2, arg3, arg4, arg5, arg6)
	ret0, _ := ret[0].(*connector.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *ConnectorServiceMockRecorder) Create(arg0, arg1, arg2, arg3, arg4, arg5, arg6 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*ConnectorService)(nil).Create), arg0, arg1, arg2, arg3, arg4, arg5, arg6)
}

// Delete mocks base method.
func (m *ConnectorService) Delete(arg0 context.Context, arg1 string, arg2 connector.PluginDispenserFetcher) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *ConnectorServiceMockRecorder) Delete(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*ConnectorService)(nil).Delete), arg0, arg1, arg2)
}

// Get mocks base method.
func (m *ConnectorService) Get(arg0 context.Context, arg1 string) (*connector.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1)
	ret0, _ := ret[0].(*connector.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *ConnectorServiceMockRecorder) Get(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*ConnectorService)(nil).Get), arg0, arg1)
}

// List mocks base method.
func (m *ConnectorService) List(arg0 context.Context) map[string]*connector.Instance {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]*connector.Instance)
	return ret0
}

// List indicates an expected call of List.
func (mr *ConnectorServiceMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*ConnectorService)(nil).List), arg0)
}

// RemoveProcessor mocks base method.
func (m *ConnectorService) RemoveProcessor(arg0 context.Context, arg1, arg2 string) (*connector.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveProcessor", arg0, arg1, arg2)
	ret0, _ := ret[0].(*connector.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// RemoveProcessor indicates an expected call of RemoveProcessor.
func (mr *ConnectorServiceMockRecorder) RemoveProcessor(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveProcessor", reflect.TypeOf((*ConnectorService)(nil).RemoveProcessor), arg0, arg1, arg2)
}

// Update mocks base method.
func (m *ConnectorService) Update(arg0 context.Context, arg1 string, arg2 connector.Config) (*connector.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", arg0, arg1, arg2)
	ret0, _ := ret[0].(*connector.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *ConnectorServiceMockRecorder) Update(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*ConnectorService)(nil).Update), arg0, arg1, arg2)
}

// ProcessorService is a mock of ProcessorService interface.
type ProcessorService struct {
	ctrl     *gomock.Controller
	recorder *ProcessorServiceMockRecorder
}

// ProcessorServiceMockRecorder is the mock recorder for ProcessorService.
type ProcessorServiceMockRecorder struct {
	mock *ProcessorService
}

// NewProcessorService creates a new mock instance.
func NewProcessorService(ctrl *gomock.Controller) *ProcessorService {
	mock := &ProcessorService{ctrl: ctrl}
	mock.recorder = &ProcessorServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ProcessorService) EXPECT() *ProcessorServiceMockRecorder {
	return m.recorder
}

// Create mocks base method.
func (m *ProcessorService) Create(arg0 context.Context, arg1, arg2 string, arg3 processor.Parent, arg4 processor.Config, arg5 processor.ProvisionType, arg6 string) (*processor.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", arg0, arg1, arg2, arg3, arg4, arg5, arg6)
	ret0, _ := ret[0].(*processor.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *ProcessorServiceMockRecorder) Create(arg0, arg1, arg2, arg3, arg4, arg5, arg6 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*ProcessorService)(nil).Create), arg0, arg1, arg2, arg3, arg4, arg5, arg6)
}

// Delete mocks base method.
func (m *ProcessorService) Delete(arg0 context.Context, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *ProcessorServiceMockRecorder) Delete(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*ProcessorService)(nil).Delete), arg0, arg1)
}

// Get mocks base method.
func (m *ProcessorService) Get(arg0 context.Context, arg1 string) (*processor.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", arg0, arg1)
	ret0, _ := ret[0].(*processor.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *ProcessorServiceMockRecorder) Get(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*ProcessorService)(nil).Get), arg0, arg1)
}

// List mocks base method.
func (m *ProcessorService) List(arg0 context.Context) map[string]*processor.Instance {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]*processor.Instance)
	return ret0
}

// List indicates an expected call of List.
func (mr *ProcessorServiceMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*ProcessorService)(nil).List), arg0)
}

// MakeRunnableProcessor mocks base method.
func (m *ProcessorService) MakeRunnableProcessor(arg0 context.Context, arg1 *processor.Instance) (*processor.RunnableProcessor, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "MakeRunnableProcessor", arg0, arg1)
	ret0, _ := ret[0].(*processor.RunnableProcessor)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// MakeRunnableProcessor indicates an expected call of MakeRunnableProcessor.
func (mr *ProcessorServiceMockRecorder) MakeRunnableProcessor(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "MakeRunnableProcessor", reflect.TypeOf((*ProcessorService)(nil).MakeRunnableProcessor), arg0, arg1)
}

// Update mocks base method.
func (m *ProcessorService) Update(arg0 context.Context, arg1 string, arg2 processor.Config) (*processor.Instance, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", arg0, arg1, arg2)
	ret0, _ := ret[0].(*processor.Instance)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *ProcessorServiceMockRecorder) Update(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*ProcessorService)(nil).Update), arg0, arg1, arg2)
}

// ConnectorPluginService is a mock of ConnectorPluginService interface.
type ConnectorPluginService struct {
	ctrl     *gomock.Controller
	recorder *ConnectorPluginServiceMockRecorder
}

// ConnectorPluginServiceMockRecorder is the mock recorder for ConnectorPluginService.
type ConnectorPluginServiceMockRecorder struct {
	mock *ConnectorPluginService
}

// NewConnectorPluginService creates a new mock instance.
func NewConnectorPluginService(ctrl *gomock.Controller) *ConnectorPluginService {
	mock := &ConnectorPluginService{ctrl: ctrl}
	mock.recorder = &ConnectorPluginServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ConnectorPluginService) EXPECT() *ConnectorPluginServiceMockRecorder {
	return m.recorder
}

// List mocks base method.
func (m *ConnectorPluginService) List(arg0 context.Context) (map[string]connector0.Specification, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]connector0.Specification)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *ConnectorPluginServiceMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*ConnectorPluginService)(nil).List), arg0)
}

// NewDispenser mocks base method.
func (m *ConnectorPluginService) NewDispenser(arg0 log.CtxLogger, arg1 string) (connector0.Dispenser, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewDispenser", arg0, arg1)
	ret0, _ := ret[0].(connector0.Dispenser)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// NewDispenser indicates an expected call of NewDispenser.
func (mr *ConnectorPluginServiceMockRecorder) NewDispenser(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewDispenser", reflect.TypeOf((*ConnectorPluginService)(nil).NewDispenser), arg0, arg1)
}

// ValidateDestinationConfig mocks base method.
func (m *ConnectorPluginService) ValidateDestinationConfig(arg0 context.Context, arg1 string, arg2 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ValidateDestinationConfig", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// ValidateDestinationConfig indicates an expected call of ValidateDestinationConfig.
func (mr *ConnectorPluginServiceMockRecorder) ValidateDestinationConfig(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ValidateDestinationConfig", reflect.TypeOf((*ConnectorPluginService)(nil).ValidateDestinationConfig), arg0, arg1, arg2)
}

// ValidateSourceConfig mocks base method.
func (m *ConnectorPluginService) ValidateSourceConfig(arg0 context.Context, arg1 string, arg2 map[string]string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ValidateSourceConfig", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// ValidateSourceConfig indicates an expected call of ValidateSourceConfig.
func (mr *ConnectorPluginServiceMockRecorder) ValidateSourceConfig(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ValidateSourceConfig", reflect.TypeOf((*ConnectorPluginService)(nil).ValidateSourceConfig), arg0, arg1, arg2)
}

// ProcessorPluginService is a mock of ProcessorPluginService interface.
type ProcessorPluginService struct {
	ctrl     *gomock.Controller
	recorder *ProcessorPluginServiceMockRecorder
}

// ProcessorPluginServiceMockRecorder is the mock recorder for ProcessorPluginService.
type ProcessorPluginServiceMockRecorder struct {
	mock *ProcessorPluginService
}

// NewProcessorPluginService creates a new mock instance.
func NewProcessorPluginService(ctrl *gomock.Controller) *ProcessorPluginService {
	mock := &ProcessorPluginService{ctrl: ctrl}
	mock.recorder = &ProcessorPluginServiceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *ProcessorPluginService) EXPECT() *ProcessorPluginServiceMockRecorder {
	return m.recorder
}

// List mocks base method.
func (m *ProcessorPluginService) List(arg0 context.Context) (map[string]sdk.Specification, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", arg0)
	ret0, _ := ret[0].(map[string]sdk.Specification)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *ProcessorPluginServiceMockRecorder) List(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*ProcessorPluginService)(nil).List), arg0)
}

// NewProcessor mocks base method.
func (m *ProcessorPluginService) NewProcessor(arg0 context.Context, arg1, arg2 string) (sdk.Processor, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewProcessor", arg0, arg1, arg2)
	ret0, _ := ret[0].(sdk.Processor)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// NewProcessor indicates an expected call of NewProcessor.
func (mr *ProcessorPluginServiceMockRecorder) NewProcessor(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewProcessor", reflect.TypeOf((*ProcessorPluginService)(nil).NewProcessor), arg0, arg1, arg2)
}
