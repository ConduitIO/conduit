// Code generated by MockGen. DO NOT EDIT.
// Source: encode.go
//
// Generated by this command:
//
//	mockgen -typed -source encode.go -destination=mock_encoder.go -package=avro -mock_names=encoder=MockEncoder . encoder
//

// Package avro is a generated GoMock package.
package avro

import (
	context "context"
	reflect "reflect"

	opencdc "github.com/conduitio/conduit-commons/opencdc"
	gomock "go.uber.org/mock/gomock"
)

// MockEncoder is a mock of encoder interface.
type MockEncoder struct {
	ctrl     *gomock.Controller
	recorder *MockEncoderMockRecorder
}

// MockEncoderMockRecorder is the mock recorder for MockEncoder.
type MockEncoderMockRecorder struct {
	mock *MockEncoder
}

// NewMockEncoder creates a new mock instance.
func NewMockEncoder(ctrl *gomock.Controller) *MockEncoder {
	mock := &MockEncoder{ctrl: ctrl}
	mock.recorder = &MockEncoderMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockEncoder) EXPECT() *MockEncoderMockRecorder {
	return m.recorder
}

// Encode mocks base method.
func (m *MockEncoder) Encode(ctx context.Context, sd opencdc.StructuredData) (opencdc.RawData, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Encode", ctx, sd)
	ret0, _ := ret[0].(opencdc.RawData)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Encode indicates an expected call of Encode.
func (mr *MockEncoderMockRecorder) Encode(ctx, sd any) *MockEncoderEncodeCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Encode", reflect.TypeOf((*MockEncoder)(nil).Encode), ctx, sd)
	return &MockEncoderEncodeCall{Call: call}
}

// MockEncoderEncodeCall wrap *gomock.Call
type MockEncoderEncodeCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *MockEncoderEncodeCall) Return(arg0 opencdc.RawData, arg1 error) *MockEncoderEncodeCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *MockEncoderEncodeCall) Do(f func(context.Context, opencdc.StructuredData) (opencdc.RawData, error)) *MockEncoderEncodeCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *MockEncoderEncodeCall) DoAndReturn(f func(context.Context, opencdc.StructuredData) (opencdc.RawData, error)) *MockEncoderEncodeCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}
