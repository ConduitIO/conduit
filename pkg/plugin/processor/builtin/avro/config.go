// Copyright © 2024 Meroxa, Inc.
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

package avro

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/avro/schemaregistry"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"os"
)

type encodeConfig struct {
	// Field is the field that will be encoded.
	Field string `json:"field" default:".Payload.After"`

	// URL of the schema registry (e.g. http://localhost:8085)
	URL string `json:"url" validate:"required"`

	// SchemaStrategy specifies which strategy to use to determine the schema for the record.
	// Available strategies are:
	// * `preRegistered` (recommended) - Download an existing schema from the schema registry.
	//    This strategy is further configured with options starting with `schema.preRegistered.*`.
	// * `autoRegister` (for development purposes) - Infer the schema from the record and register it
	//    in the schema registry. This strategy is further configured with options starting with
	//   `schema.autoRegister.*`.
	//
	// For more information about the behavior of each strategy read the main processor description.
	SchemaStrategy string `json:"schema.strategy" validate:"required,inclusion=preRegistered|autoRegister"`

	PreRegistered struct {
		// Subject specifies the subject of the schema in the schema registry used to encode the record.
		Subject string `json:"subject"`
		// Version specifies the version of the schema in the schema registry used to encode the record.
		// todo validations ok?
		Version int `json:"version" validate:"gt=0"`
	} `json:"schema.preRegistered"`

	// AutoRegisteredSubject specifies the subject name under which the inferred schema will be registered
	// in the schema registry.
	AutoRegisteredSubject string `json:"schema.autoRegistered.subject"`

	Auth struct {
		// Username is the username to use with basic authentication. This option is required if
		// auth.basic.password contains a value. If both auth.basic.username and auth.basic.password
		// are empty basic authentication is disabled.
		Username string `json:"basic.username"`
		// Password is the password to use with basic authentication. This option is required if
		// auth.basic.username contains a value. If both auth.basic.username and auth.basic.password
		// are empty basic authentication is disabled.
		Password string `json:"basic.password"`
	} `json:"auth"`

	TLS struct {
		// CACert is the path to a file containing PEM encoded CA certificates. If this option is empty,
		// Conduit falls back to using the host's root CA set.
		CACert string `json:"ca.cert"`

		Client struct {
			// Cert is the path to a file containing a PEM encoded certificate. This option is required
			// if tls.client.key contains a value. If both tls.client.cert and tls.client.key are empty
			// TLS is disabled.
			Cert string `json:"cert"`
			// Key is the path to a file containing a PEM encoded private key. This option is required
			// if tls.client.cert contains a value. If both tls.client.cert and tls.client.key are empty
			// TLS is disabled.
			Key string `json:"key"`
		} `json:"client"`
	} `json:"tls"`

	tlsClientCert *tls.Certificate
	tlsCACert     *x509.CertPool
	strategy      schemaregistry.SchemaStrategy
	fieldResolver sdk.ReferenceResolver
}

func (c *encodeConfig) validateBasicAuth() error {
	switch {
	case c.Auth.Username == "" && c.Auth.Password == "":
		// no basic auth set
		return nil
	case c.Auth.Username == "":
		return cerrors.Errorf("specify a username to enable basic auth or remove field password")
	case c.Auth.Password == "":
		return cerrors.Errorf("specify a password to enable basic auth or remove field username")
	}

	return nil
}

func (c *encodeConfig) parseTLS() error {
	if c.TLS.Client.Cert == "" && c.TLS.Client.Key == "" && c.TLS.CACert == "" {
		// no tls config set
		return nil
	} else if c.TLS.Client.Cert == "" || c.TLS.Client.Key == "" {
		// we are missing some configuration fields
		err := cerrors.New("invalid TLS config")
		if c.TLS.Client.Cert == "" {
			err = multierror.Append(err, cerrors.New("missing field: tls.client.cert"))
		}
		if c.TLS.Client.Key == "" {
			err = multierror.Append(err, cerrors.New("missing field: tls.client.key"))
		}
		// CA cert is optional, we don't check if it's missing
		return err
	}

	clientCert, err := tls.LoadX509KeyPair(c.TLS.Client.Cert, c.TLS.Client.Key)
	if err != nil {
		return fmt.Errorf("failed to load client certificate: %w", err)
	}

	c.tlsClientCert = &clientCert

	if c.TLS.Client.Cert != "" {
		// load custom CA cert
		caCert, err := os.ReadFile(c.TLS.Client.Cert)
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

func (c *encodeConfig) ClientOptions() []sr.Opt {
	clientOpts := []sr.Opt{sr.URLs(c.URL), sr.Normalize()}
	if c.Auth.Username != "" && c.Auth.Password != "" {
		clientOpts = append(clientOpts, sr.BasicAuth(c.Auth.Username, c.Auth.Password))
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

func (c *encodeConfig) parseSchemaStrategy() error {
	switch c.SchemaStrategy {
	case "preRegistered":
		return c.parseSchemaStrategyPreRegistered()
	case "autoRegister":
		return c.parseSchemaStrategyAutoRegister()
	default:
		return cerrors.Errorf("unknown schema strategy %q", c.SchemaStrategy)
	}
}

func (c *encodeConfig) parseSchemaStrategyPreRegistered() error {
	// TODO allow version to be set to "latest"
	if c.PreRegistered.Subject == "" {
		return cerrors.New("subject required for schema strategy 'preRegistered")
	}
	c.strategy = schemaregistry.DownloadSchemaStrategy{
		Subject: c.PreRegistered.Subject,
		Version: c.PreRegistered.Version,
	}
	return nil
}

func (c *encodeConfig) parseSchemaStrategyAutoRegister() error {
	if c.AutoRegisteredSubject == "" {
		return cerrors.New("subject required for schema strategy 'autoRegister")
	}

	c.strategy = schemaregistry.ExtractAndUploadSchemaStrategy{
		Type:    sr.TypeAvro,
		Subject: c.AutoRegisteredSubject,
	}
	return nil
}

func (c *encodeConfig) parseTargetField() error {
	rr, err := sdk.NewReferenceResolver(c.Field)
	if err != nil {
		return err
	}

	c.fieldResolver = rr
	return nil
}
