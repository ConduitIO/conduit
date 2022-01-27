# S3 Connector

## Source
The S3 Source Connector connects to a S3 bucket with the provided configurations, using
`aws.bucket`, `aws.access-key-id`,`aws.secret-access-key` and `aws.region`. Then will
call `Configure` to parse the configurations and make sure the bucket exists, If the
bucket doesn't exist, or the permissions fail, then an error will occur. After that, the
`Open` method is called to start the connection from the provided position.

### Change Data Capture (CDC)
This connector implements CDC features for S3 by scanning the bucket for changes every
`polling-period` and detecting any change that happened after a certain timestamp. These
changes (update, delete, insert) are then inserted into a buffer that is checked on each
Read request.
* To capture "delete" actions, the S3 bucket versioning must be enabled, and the output
  record will have a metadata of `"action":"delete"`.
* To capture "insert" or "update" actions, the bucket versioning doesn't matter, and no
  metadata is added for these actions.

### Testing
You must set the environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`)
before you can run the tests.
The tests have the tag "integration", so they can be run using `make test-integration`.


#### Position Handling
The connector goes through two modes.
* Snapshot mode: which loops through the S3 bucket and returns the objects that are
  already in there. The _Position_ during this mode is the object key attached to
  an underscore, an "s" for snapshot, and the _maxLastModifiedDate_ found so far.
  As an example: "thisIsAKey_s12345", which makes the connector know at what
  mode it is and what object it last read. The _maxLastModifiedDate_ will be used when
  changing to CDC mode, the iterator will capture changes that happened after that.

* CDC mode: this mode iterates through the S3 bucket every `polling-period` and captures
  new actions made on the bucket. the _Position_ during this mode is the object key
  attached to an underscore, a "c" for CDC, and the object's _lastModifiedDate_ in seconds.
  As an example: "thisIsAKey_c1634049397".
  This position is used to return only the actions with a _lastModifiedDate_ higher than
  the last record returned, which will ensure that no duplications are in place.


### Record Keys
The S3 object key uniquely identifies the objects in an Amazon S3 bucket, which is why a
record key is the key read from the S3 bucket.

### Configuration
The config passed to `Open` can contain the following fields.

| name                  | description                                                                            | required  | example             |
|-----------------------|----------------------------------------------------------------------------------------|-----------|---------------------|
| aws.access-key-id     | AWS access key id                                                                      | yes       | "THE_ACCESS_KEY_ID" |
| aws.secret-access-key | AWS secret access key                                                                  | yes       | "SECRET_ACCESS_KEY" |
| aws.region            | the AWS S3 bucket region                                                               | yes       | "us-east-1"         |
| aws.bucket            | the AWS S3 bucket name                                                                 | yes       | "bucket_name"       |
| polling-period        | polling period for the CDC mode, formatted as a time.Duration string. default is "1s"  | no        | "2s", "500ms"       |


### Known Limitations
* If a pipeline restarts during the snapshot, then the connector will start scanning the
  objects from the beginning of the bucket, which could result in duplications.


## Destination
The S3 Destination Connector connects to an S3 bucket with the provided configurations, using
`aws.bucket`, `aws.access-key-id`,`aws.secret-access-key` and `aws.region`. Then will
call `Configure` to parse the configurations, If parsing was not successful, then an 
error will occur. After that, the `Open` method is called to start the connection. If
the permissions fail, the connector will not be ready for writing to S3.

### Writer
The S3 destination writer has a buffer with the size of `buffer-size`, for each time
`Write` is called, a new record is added to the buffer. When the buffer is full,
all the records from it will be written to the S3 bucket, and an ack function will be
called for each record after being written.

### Configuration
The config passed to `Open` can contain the following fields.

| name                  | description                                                                                                     | required | example             |
|-----------------------|-----------------------------------------------------------------------------------------------------------------|----------|---------------------|
| aws.access-key-id     | AWS access key id                                                                                               | yes      | "THE_ACCESS_KEY_ID" |
| aws.secret-access-key | AWS secret access key                                                                                           | yes      | "SECRET_ACCESS_KEY" |
| aws.region            | the AWS S3 bucket region                                                                                        | yes      | "us-east-1"         |
| aws.bucket            | the AWS S3 bucket name                                                                                          | yes      | "bucket_name"       |
| format                | the destination format, either "json" or "parquet"                                                              | yes      | "json"              |
| buffer-size           | the buffer size {when full, the files will be written to destination}, default is "1000", max is "100000"       | no       | "100"               |
| prefix                | the key prefix for S3 destination                                                                               | no       | "conduit-"          |