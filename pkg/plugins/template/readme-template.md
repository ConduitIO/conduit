### Overview
{{.Summary}}

Current version: {{.Version}}

Author:          {{.Author}}
{{.Description}}
### Development
_A section about the development flow, any particular notes about development, such as: language and frameworks used,
code style, testing etc._

### How to build it
_A section with instructions how to build the plugin, available build options etc._

### Source
_A section about the source connector in this plugin, its capabilities, modes (e.g. does it support CDC) etc._

#### Configuration
_The source connector's configuration. It will be automatically populated from the plugin's specification
when `make readme-pluginName` is executed._

|Name|Description|Default|Required|
|---|---|---|---|
{{range $name, $param := .SourceParams}} |{{$name}}|{{$param.Description | cellValue}}|{{$param.Default | cellValue}}|{{$param.Required}}| 
{{end}}

### Destination
_A section about the destination connector in this plugin and its capabilities._

#### Configuration
_The destination connector's configuration. It will be automatically populated from the plugin's specification
when `make readme-pluginName` is executed._

|Name|Description|Default|Required|
|---|---|---|---|
{{range $name, $param := .DestinationParams}} |{{$name}}|{{$param.Description | cellValue}}|{{$param.Default | cellValue}}|{{$param.Required}}| 
{{end}}

### Planned improvements
_A list of planned features and improvements, potentially the roadmap too._

### Known issues

### References
