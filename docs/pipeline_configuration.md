## Overview
Creating pipelines in Conduit is a simple process with many steps to define the resources and their configurations, this
could be done through API or Conduit's UI. But to make this process even simpler and to make it possible to load 
pre-defined pipelines, we introduce **_Pipeline Configurations_**.

Using **_Pipeline Configurations_** you will define all the pipelines you want to be provisioned into Conduit at startup,
only by creating `.yml` files that define all the pipelines, with their connectors, processors, and configurations.

## Getting started
create a folder called _"pipelines"_ at the same level as your Conduit binary file, add all your `.yml` files there, 
then run Conduit using the cmd command: 
```
./conduit
```
If you have your YAML files at a different directory, simply run conduit with the cli flag `pipelines.path` and point to 
your directory, ex: 
```
./conduit -pipeline.path ../my-directory
```

_**Note**_: provisioned pipelines are **immutable**, any updates needed on a provisioned pipeline have to be done through
the configuration file it was provisioned from. you can only control stopping and starting a pipeline through the UI and API.

### YAML file format
The file in general has two root keys, the "version", and the "pipelines" map. The map consists of other elements like 
"status" and "name", which are configurations for the pipeline itself.

To create connectors in that pipeline, simply add another map under the pipeline map, and call it "connectors".

To create processors, either add a "processors" map under a pipeline ID, or under a connector ID, depending on its parent.
check this YAML file example with explanation for each field:

``` yaml
version: 1.0                    # parser version, the only supported version for now is 1.0 [mandatory]
pipelines:                      # a map of pipelines IDs and their configurations.
  pipeline1:                    # pipeline ID, has to be unique.
    status: running             # pipelines status at startup, either running or stopped. [mandatory]
    name: pipeline1             # pipeline name. [optional]
    description: desc           # pipeline describtion. [optional]
    connectors:                 # a map of connectors IDs and their configurations.
      con1:                     # connector ID, has to be unieuq per pipeline.
        type: source            # connector type, either "source" or "destination". [mandatory]
        plugin: builtin:file    # connector plugin. [mandatory]
        name: con3              # connector name. [optional]
        settings:               # map of configurations keys and their values.
          path: ./file1.txt     # for this example, the plugin "bultin:file" has only one configuration, which is path.
      con2:
        type: destination
        plugin: builtin:file
        name: file-dest
        settings:
          path: ./file2.txt
        processors:             # a map of processor IDs and their configurations, "con2" is the processor parent.
          proc1:                 # processor ID, has to be unique for each parent
            type: js            # processor type. [mandatory]
            settings:           # map of processor configurations and values
              Prop1: string
    processors:                 # processor IDs, that have the pipeline "pipeline1" as a parent.
      proc2: 
        type: js
        settings:
          additionalProp1: string
          additionalProp2: string
```

### Updates and Deletes
Updates and deletes for a provisioned pipeline can only be done through the configuration files.

* To delete a pipeline: simply delete it from the pipelines map from the configuration file, then run conduit again.
* To update a pipeline: change any field value from the configuration file, and run conduit again to address these updates.
  
Note: Updates would keep the status of the pipeline and it would continue working from where it stopped. However, 
  If the Updated configurations were something like "connector-id", "plugin", "pipeline-id", or "processor-id", then 
  this would result in a pipeline starting from the beginning of the source, since the old position won't be copied.

