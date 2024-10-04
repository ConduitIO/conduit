// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package log

const (
	ComponentField        = "component"
	ConnectorIDField      = "connector_id"
	AttemptField          = "attempt"
	DurationField         = "duration"
	MessageIDField        = "message_id"
	NodeIDField           = "node_id"
	ParallelWorkerIDField = "parallel_worker_id"
	PipelineIDField       = "pipeline_id"
	PipelineStatusField   = "pipeline_status"
	ProcessorIDField      = "processor_id"
	RecordPositionField   = "record_position"
	RequestIDField        = "request_id"
	ServerAddressField    = "address"

	GRPCMethodField     = "grpc_method"
	GRPCStatusCodeField = "grpc_status_code"
	HTTPEndpointField   = "http_endpoint"

	PluginTypeField = "plugin_type"
	PluginNameField = "plugin_name"
	PluginPathField = "plugin_path"

	FilepathField = "filepath"

	InspectorSessionID = "inspector_session_id"
)
