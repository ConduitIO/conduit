## Pipeline Configuration Files
Creating pipelines in Conduit consists of many steps to define the resources and their configurations, this
could be done through API or Conduit's UI. But to make this process simpler and to make it possible to load 
pre-defined pipelines, we introduce **_Pipeline Configuration Files_**.

Using pipeline configuration files you can define pipelines that are provisioned by Conduit at startup.
It's as simple as creating a YAML file that defines pipelines, connectors, processors, and their corresponding configurations.

## Getting started
Create a folder called `pipelines` at the same level as your Conduit binary file, add all your YAML files 
there, then run Conduit using the command: 
```
./conduit
```
Conduit will only search for files with `.yml` or `.yaml` extensions, recursively in all sub-folders. 

If you have your YAML files in a different directory, or want to provision only one file, then simply run Conduit with 
the CLI flag `pipelines.path` and point to your file or directory:
```
./conduit -pipeline.path ../my-directory
```
If your directory does not exist, Conduit will fail with an error: `"pipelines.path" config value is invalid`

### YAML Schema
The file in general has two root keys, the `version`, and the `pipelines` map. The map consists of other elements like 
`status` and `name`, which are configurations for the pipeline itself.

To create connectors in that pipeline, simply add another map under the pipeline map, and call it `connectors`.

To create processors, either add a `processors` map under a pipeline ID, or under a connector ID, depending on its parent.
Check this YAML file example with explanation for each field:

``` yaml
version: 1.0                    # parser version, the only supported version for now is 1.0 [mandatory]

pipelines:                      # a map of pipelines IDs and their configurations.
  pipeline1:                    # pipeline ID, has to be unique.
    status: running             # pipelines status at startup, either running or stopped. [mandatory]
    name: pipeline1             # pipeline name, if not specified, pipeline ID will be used as name. [optional]
    description: desc           # pipeline description. [optional]
    connectors:                 # a map of connectors IDs and their configurations.
      con1:                     # connector ID, has to be unique per pipeline.
        type: source            # connector type, either "source" or "destination". [mandatory]
        plugin: builtin:file    # connector plugin. [mandatory]
        name: con3              # connector name, if not specified, connector ID will be used as name. [optional]
        settings:               # map of configurations keys and their values.
          path: ./file1.txt     # for this example, the plugin "bultin:file" has only one configuration, which is path.
      con2:
        type: destination
        plugin: builtin:file
        name: file-dest
        settings:
          path: ./file2.txt
        processors:             # a map of processor IDs and their configurations, "con2" is the processor parent.
          proc1:                # processor ID, has to be unique for each parent
            type: js            # processor type. [mandatory]
            settings:           # map of processor configurations and values
              Prop1: string
    processors:                 # processor IDs, that have the pipeline "pipeline1" as a parent.
      proc2: 
        type: js
        settings:
          prop1: ${ENV_VAR}     # yon can use environmental variables by wrapping them in a dollar sign and curly braces ${}.
```

If the file is invalid (missed a mandatory field, or has an invalid configuration value), then the pipeline that has the
invalid value will be skipped, with an error message logged.

If two pipelines in one file have the same ID, or the `version` field was not specified, then the file would be 
non-parsable and will be skipped with an error message logged.

If two pipelines from different files have the same ID, the second pipeline will be skipped, with an error message
specifying which pipeline was not provisioned.

**_Note_**: Connector IDs and processor IDs will get their parent ID prefixed, so if you specify a connector ID as `con1` 
and its parent is `pipeline1`, then the provisioned connector will have the ID `pipeline1:con1`. Same goes for processors, 
if the processor has a pipeline parent, then the processor ID will be `connectorID:processorID`, and if a processor 
has a connector parent, then the processor ID will be `pipelineID:connectorID:processorID`. 

## Pipelines Immutability
Pipelines provisioned by configuration files are **immutable**, any updates needed on a provisioned pipeline have to be 
done through the configuration file it was provisioned from. You can only control stopping and starting a pipeline 
through the UI or API.

### Updates and Deletes
Updates and deletes for a pipeline provisioned by configuration files can only be done through the configuration files.
Changes should be made to the files, then Conduit has to be restarted to reload the changes. Any updates or deletes done
through the API or UI will be prohibited.

* To delete a pipeline: simply delete it from the `pipelines` map from the configuration file, then run conduit again.
* To update a pipeline: change any field value from the configuration file, and run conduit again to address these updates.
  
Updates will preserve the status of the pipeline, and will continue working from where it stopped. However, the pipeline
will start from the beginning of the source and will not continue from where it stopped, if one of these values were updated:
{`pipeline ID`, `connector ID`, `connector plugin`, `connector type`}.


