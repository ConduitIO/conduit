// Copyright © 2023 Meroxa, Inc.
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

package procbuiltin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/schemaregistry"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"github.com/rs/zerolog"
)

const (
	decodeWithSchemaKeyProcType     = "decodewithschemakey"
	decodeWithSchemaPayloadProcType = "decodewithschemapayload"

	schemaRegistryConfigURL               = "url"
	schemaRegistryConfigAuthBasicUsername = "auth.basic.username"
	schemaRegistryConfigAuthBasicPassword = "auth.basic.password" //nolint:gosec // false positive, these are not credentials
	schemaRegistryConfigTLSCACert         = "tls.ca.cert"
	schemaRegistryConfigTLSClientCert     = "tls.client.cert"
	schemaRegistryConfigTLSClientKey      = "tls.client.key"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(decodeWithSchemaKeyProcType, DecodeWithSchemaKey)
	processor.GlobalBuilderRegistry.MustRegister(decodeWithSchemaPayloadProcType, DecodeWithSchemaPayload)
}

// DecodeWithSchemaKey builds a processor with the following config fields:
//   - `url` (Required) - URL of the schema registry (e.g. http://localhost:8085)
//   - `auth.basic.username` (Optional) - Configures the username to use with
//     basic authentication. This option is required if `auth.basic.password`
//     contains a value. If both `auth.basic.username` and `auth.basic.password`
//     are empty basic authentication is disabled.
//   - `auth.basic.password` (Optional) - Configures the password to use with
//     basic authentication. This option is required if `auth.basic.username`
//     contains a value. If both `auth.basic.username` and `auth.basic.password`
//     are empty basic authentication is disabled.
//   - `tls.ca.cert` (Optional) - Path to a file containing PEM encoded CA
//     certificates. If this option is empty, Conduit falls back to using the
//     host's root CA set.
//   - `tls.client.cert` (Optional) - Path to a file containing a PEM encoded
//     certificate. This option is required if `tls.client.key` contains a value.
//     If both `tls.client.cert` and `tls.client.key` are empty TLS is disabled.
//   - `tls.client.key` (Optional) - Path to a file containing a PEM encoded
//     private key. This option is required if `tls.client.cert` contains a value.
//     If both `tls.client.cert` and `tls.client.key` are empty TLS is disabled.
//
// The processor takes raw data (bytes) and decodes it from the Confluent wire
// format into structured data. It extracts the schema ID from the data,
// downloads the associated schema from the schema registry and decodes the
// payload. The schema is cached locally after it's first downloaded. Currently,
// the processor only supports the Avro format. If the processor encounters
// structured data or the data can't be decoded it returns an error.
//
// More info about the Confluent wire format: https://docs.confluent.io/platform/current/schema-registry/fundamentals/serdes-develop/index.html#wire-format
// More info about the Confluent schema registry: https://docs.confluent.io/platform/current/schema-registry/index.html
func DecodeWithSchemaKey(config processor.Config) (processor.Interface, error) {
	return decodeWithSchema(decodeWithSchemaKeyProcType, recordKeyGetSetter{}, config)
}

// DecodeWithSchemaPayload builds the same processor as DecodeWithSchemaKey,
// except that it operates on the field Record.Payload.After.
func DecodeWithSchemaPayload(config processor.Config) (processor.Interface, error) {
	return decodeWithSchema(decodeWithSchemaPayloadProcType, recordPayloadGetSetter{}, config)
}

type schemaRegistryConfig struct {
	url string

	basicAuthUsername string
	basicAuthPassword string

	tlsCACert     *x509.CertPool
	tlsClientCert *tls.Certificate
}

func (c *schemaRegistryConfig) Parse(cfg processor.Config) error {
	var err error
	if c.url, err = getConfigFieldString(cfg, schemaRegistryConfigURL); err != nil {
		return err
	}
	if err = c.parseBasicAuth(cfg); err != nil {
		return err
	}
	return c.parseTLS(cfg)
}

func (c *schemaRegistryConfig) ClientOptions() []sr.Opt {
	clientOpts := []sr.Opt{sr.URLs(c.url), sr.Normalize()}
	if c.basicAuthUsername != "" && c.basicAuthPassword != "" {
		clientOpts = append(clientOpts, sr.BasicAuth(c.basicAuthUsername, c.basicAuthPassword))
	}
	if c.tlsClientCert != nil {
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{*c.tlsClientCert},
			MinVersion:   tls.VersionTLS12,
		}
		if c.tlsCACert != nil {
			tlsConfig.RootCAs = c.tlsCACert
		}
		clientOpts = append(clientOpts, sr.DialTLSConfig(tlsConfig))
	}
	return clientOpts
}

func (c *schemaRegistryConfig) parseBasicAuth(cfg processor.Config) error {
	username := cfg.Settings[schemaRegistryConfigAuthBasicUsername]
	password := cfg.Settings[schemaRegistryConfigAuthBasicPassword]

	switch {
	case username == "" && password == "":
		// no basic auth set
		return nil
	case username == "":
		return cerrors.Errorf("missing field %q: specify a username to enable basic auth or remove field %q", schemaRegistryConfigAuthBasicUsername, schemaRegistryConfigAuthBasicPassword)
	case password == "":
		return cerrors.Errorf("missing field %q: specify a password to enable basic auth or remove field %q", schemaRegistryConfigAuthBasicPassword, schemaRegistryConfigAuthBasicUsername)
	}
	c.basicAuthUsername = username
	c.basicAuthPassword = password
	return nil
}

func (c *schemaRegistryConfig) parseTLS(cfg processor.Config) error {
	clientCertPath := cfg.Settings[schemaRegistryConfigTLSClientCert]
	clientKeyPath := cfg.Settings[schemaRegistryConfigTLSClientKey]
	caCertPath := cfg.Settings[schemaRegistryConfigTLSCACert]

	if clientCertPath == "" && clientKeyPath == "" && caCertPath == "" {
		// no tls config set
		return nil
	} else if clientCertPath == "" || clientKeyPath == "" {
		// we are missing some configuration fields
		err := cerrors.New("invalid TLS config")
		if clientCertPath == "" {
			err = multierror.Append(err, cerrors.Errorf("missing field %q", schemaRegistryConfigTLSClientCert))
		}
		if clientKeyPath == "" {
			err = multierror.Append(err, cerrors.Errorf("missing field %q", schemaRegistryConfigTLSClientKey))
		}
		// CA cert is optional, we don't check if it's missing
		return err
	}

	clientCert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load client certificate: %w", err)
	}
	c.tlsClientCert = &clientCert

	if caCertPath != "" {
		// load custom CA cert
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			return fmt.Errorf("failed to load CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			return cerrors.New("invalid CA cert")
		}
		c.tlsCACert = caCertPool
	}

	return nil
}

func decodeWithSchema(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	var c schemaRegistryConfig
	err := c.Parse(config)
	if err != nil {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}

	// TODO get logger from config or some other place
	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)

	client, err := schemaregistry.NewClient(logger, c.ClientOptions()...)
	if err != nil {
		return nil, cerrors.Errorf("%s: could not create schema registry client: %w", processorType, err)
	}
	decoder := schemaregistry.NewDecoder(client, logger, &sr.Serde{})

	return NewFuncWrapper(func(ctx context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			sd, err := decoder.Decode(ctx, d)
			if err != nil {
				return record.Record{}, cerrors.Errorf("%s: %w:", processorType, err)
			}
			r = getSetter.Set(r, sd)
			return r, nil
		case record.StructuredData:
			return record.Record{}, cerrors.Errorf("%s: structured data not supported", processorType)
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}
	}), nil
}
