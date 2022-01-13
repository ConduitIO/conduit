### General
The Conduit File plugin provides both, a destination and source File 
connector, for Conduit.

### How it works?
The Source connector listens for changes appended to the source file and 
sends records with the changes.
The Destination connector receives records and writes them to a file.

#### Source
The Source connector only cares to have a valid path, even if the file 
doesn't exist, it will still run and wait until a file with the configured
name is there, then it will start listening to changes and sending records.

#### Destination
The Destination connector will create the file if it doesn't exist, and 
records with changes will be appended to the destination file when received.

### How to build?
Run `make build-file-plugin`.

### Configuration

| name | part of | description | required | default value |
|------|---------|-------------|----------|---------------|
|`path`|destination, source|The path to the file|true| |

### Limitations
* The  Source connector only detects appended changes to the file, so it
  doesn't detect deletes or edits.
* The connectors can only access local files on the machine where Conduit
  is running. So, running Conduit on a server means it can't access a file
  on your local machine.
* Currently, only works reliably with text files (may work with non-text
  files, but not guaranteed)


