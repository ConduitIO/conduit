module github.com/conduitio/conduit

go 1.20

require (
	buf.build/gen/go/grpc-ecosystem/grpc-gateway/protocolbuffers/go v1.31.0-20230607214310-11c9972ea0fd.1
	github.com/Masterminds/semver/v3 v3.2.1
	github.com/NYTimes/gziphandler v1.1.1
	github.com/antchfx/jsonquery v1.3.3
	github.com/bufbuild/buf v1.25.0
	github.com/conduitio/conduit-connector-file v0.6.0
	github.com/conduitio/conduit-connector-generator v0.5.0
	github.com/conduitio/conduit-connector-kafka v0.6.0
	github.com/conduitio/conduit-connector-log v0.3.0
	github.com/conduitio/conduit-connector-postgres v0.5.0
	github.com/conduitio/conduit-connector-protocol v0.5.0
	github.com/conduitio/conduit-connector-s3 v0.5.0
	github.com/conduitio/conduit-connector-sdk v0.7.1
	github.com/conduitio/yaml/v3 v3.3.0
	github.com/dgraph-io/badger/v4 v4.1.0
	github.com/dop251/goja v0.0.0-20230531210528-d7324b2d74f7
	github.com/dop251/goja_nodejs v0.0.0-20230602164024-804a84515562
	github.com/gammazero/deque v0.2.1
	github.com/golang/mock v1.6.0
	github.com/google/go-cmp v0.5.9
	github.com/google/uuid v1.3.0
	github.com/gorilla/websocket v1.5.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.16.1
	github.com/hamba/avro/v2 v2.13.0
	github.com/hashicorp/go-hclog v1.5.0
	github.com/hashicorp/go-plugin v1.4.10
	github.com/jackc/pgx/v5 v5.4.2
	github.com/jinzhu/copier v0.3.5
	github.com/jpillora/backoff v1.0.0
	github.com/lovromazgon/franz-go/pkg/sr v0.0.0-20230630140346-bb9ce3f90f4a
	github.com/matryer/is v1.4.1
	github.com/modern-go/reflect2 v1.0.2
	github.com/piotrkowalczuk/promgrpc/v4 v4.1.0
	github.com/prometheus/client_golang v1.16.0
	github.com/prometheus/client_model v0.4.0
	github.com/prometheus/common v0.44.0
	github.com/rs/zerolog v1.29.1
	github.com/twmb/go-cache v1.2.0
	go.uber.org/goleak v1.2.1
	golang.org/x/exp v0.0.0-20230713183714-613f0c0eb8a1
	golang.org/x/tools v0.11.0
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2
	google.golang.org/genproto/googleapis/api v0.0.0-20230724170836-66ad5b6ff146
	google.golang.org/grpc v1.57.0
	google.golang.org/protobuf v1.31.0
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
)

require (
	github.com/Azure/go-ansiterm v0.0.0-20230124172434-306776ec8161 // indirect
	github.com/Masterminds/goutils v1.1.1 // indirect
	github.com/Masterminds/sprig/v3 v3.2.3 // indirect
	github.com/Masterminds/squirrel v1.5.4 // indirect
	github.com/Microsoft/go-winio v0.6.1 // indirect
	github.com/antchfx/xpath v1.2.3 // indirect
	github.com/apache/arrow/go/arrow v0.0.0-20211112161151-bc219186db40 // indirect
	github.com/apache/thrift v0.17.0 // indirect
	github.com/avast/retry-go v3.0.0+incompatible // indirect
	github.com/aws/aws-sdk-go-v2 v1.19.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.4.10 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.18.28 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.13.27 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.13.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.1.35 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.4.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.3.36 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.0.27 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.9.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.1.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.9.29 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.14.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.37.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.12.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.14.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.19.3 // indirect
	github.com/aws/smithy-go v1.13.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bufbuild/connect-go v1.9.0 // indirect
	github.com/bufbuild/connect-opentelemetry-go v0.4.0 // indirect
	github.com/bufbuild/protocompile v0.5.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/containerd/stargz-snapshotter/estargz v0.14.3 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/dgraph-io/ristretto v0.1.1 // indirect
	github.com/dlclark/regexp2 v1.8.0 // indirect
	github.com/docker/cli v24.0.4+incompatible // indirect
	github.com/docker/distribution v2.8.2+incompatible // indirect
	github.com/docker/docker v24.0.4+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.8.0 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dustin/go-humanize v1.0.0 // indirect
	github.com/fatih/color v1.14.1 // indirect
	github.com/felixge/fgprof v0.9.3 // indirect
	github.com/fsnotify/fsnotify v1.5.4 // indirect
	github.com/go-chi/chi/v5 v5.0.10 // indirect
	github.com/go-logr/logr v1.2.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/gofrs/uuid v4.4.0+incompatible // indirect
	github.com/gofrs/uuid/v5 v5.0.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/glog v1.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/google/flatbuffers v2.0.0+incompatible // indirect
	github.com/google/go-containerregistry v0.15.2 // indirect
	github.com/google/pprof v0.0.0-20230705174524-200ffdc848b8 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/huandu/xstrings v1.4.0 // indirect
	github.com/imdario/mergo v0.3.13 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/chunkreader/v2 v2.0.1 // indirect
	github.com/jackc/pgconn v1.14.0 // indirect
	github.com/jackc/pgio v1.0.0 // indirect
	github.com/jackc/pglogrepl v0.0.0-20220827140821-40536e4ca386 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgproto3/v2 v2.3.2 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/pgtype v1.14.0 // indirect
	github.com/jackc/pgx/v4 v4.18.1 // indirect
	github.com/jackc/puddle/v2 v2.2.0 // indirect
	github.com/jdxcode/netrc v0.0.0-20221124155335-4616370d1a84 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.16.7 // indirect
	github.com/klauspost/pgzip v1.2.6 // indirect
	github.com/lann/builder v0.0.0-20180802200727-47ae307949d0 // indirect
	github.com/lann/ps v0.0.0-20150810152359-62de8c46ede0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/go-testing-interface v1.14.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/moby/term v0.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/morikuni/aec v1.0.0 // indirect
	github.com/nxadm/tail v1.4.8 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0-rc4 // indirect
	github.com/pierrec/lz4/v4 v4.1.17 // indirect
	github.com/pkg/browser v0.0.0-20210911075715-681adbf594b8 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pkg/profile v1.7.0 // indirect
	github.com/prometheus/procfs v0.10.1 // indirect
	github.com/rs/cors v1.9.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/segmentio/kafka-go v0.4.42 // indirect
	github.com/shopspring/decimal v1.3.1 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spf13/cast v1.5.0 // indirect
	github.com/spf13/cobra v1.7.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tetratelabs/wazero v1.3.0 // indirect
	github.com/vbatts/tar-split v0.11.3 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/xitongsys/parquet-go v1.6.2 // indirect
	github.com/xitongsys/parquet-go-source v0.0.0-20220723234337-052319f3f36b // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/otel v1.16.0 // indirect
	go.opentelemetry.io/otel/metric v1.16.0 // indirect
	go.opentelemetry.io/otel/sdk v1.16.0 // indirect
	go.opentelemetry.io/otel/trace v1.16.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	golang.org/x/crypto v0.11.0 // indirect
	golang.org/x/mod v0.12.0 // indirect
	golang.org/x/net v0.12.0 // indirect
	golang.org/x/sync v0.3.0 // indirect
	golang.org/x/sys v0.10.0 // indirect
	golang.org/x/term v0.10.0 // indirect
	golang.org/x/text v0.11.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto v0.0.0-20230706204954-ccb25ca9f130 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230724170836-66ad5b6ff146 // indirect
	gopkg.in/tomb.v1 v1.0.0-20141024135613-dd632973f1e7 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
