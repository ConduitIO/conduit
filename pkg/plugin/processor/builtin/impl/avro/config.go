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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
<<<<<<< HEAD
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/schemaregistry"
=======
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/schemaregistry"
>>>>>>> lovro/migrate-to-conduit-commons
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type preRegisteredConfig struct {
	// The subject of the schema in the schema registry used to encode the record.
	Subject string `json:"subject"`
	// The version of the schema in the schema registry used to encode the record.
	Version int `json:"version" validate:"gt=0"`
}

type schemaConfig struct {
	// Strategy to use to determine the schema for the record.
	// Available strategies are:
	// * `preRegistered` (recommended) - Download an existing schema from the schema registry.
	//    This strategy is further configured with options starting with `schema.preRegistered.*`.
	// * `autoRegister` (for development purposes) - Infer the schema from the record and register it
	//    in the schema registry. This strategy is further configured with options starting with
	//   `schema.autoRegister.*`.
	//
	// For more information about the behavior of each strategy read the main processor description.
	StrategyType string `json:"strategy" validate:"required,inclusion=preRegistered|autoRegister"`

	PreRegistered preRegisteredConfig `json:"preRegistered"`

	// The subject name under which the inferred schema will be registered in the schema registry.
	AutoRegisteredSubject string `json:"autoRegister.subject"`

	strategy schemaregistry.SchemaStrategy
}

func (c *schemaConfig) parse() error {
	switch c.StrategyType {
	case "preRegistered":
		return c.parsePreRegistered()
	case "autoRegister":
		return c.parseAutoRegister()
	default:
		return cerrors.Errorf("unknown schema strategy %q", c.StrategyType)
	}
}

func (c *schemaConfig) parsePreRegistered() error {
	if c.PreRegistered.Subject == "" {
		return cerrors.New("subject required for schema strategy 'preRegistered'")
	}
	// TODO allow version to be set to "latest"
	if c.PreRegistered.Version <= 0 {
		return cerrors.Errorf("version needs to be positive: %v", c.PreRegistered.Version)
	}

	c.strategy = schemaregistry.DownloadSchemaStrategy{
		Subject: c.PreRegistered.Subject,
		Version: c.PreRegistered.Version,
	}
	return nil
}

func (c *schemaConfig) parseAutoRegister() error {
	if c.AutoRegisteredSubject == "" {
		return cerrors.New("subject required for schema strategy 'autoRegister'")
	}

	c.strategy = schemaregistry.ExtractAndUploadSchemaStrategy{
		Type:    sr.TypeAvro,
		Subject: c.AutoRegisteredSubject,
	}
	return nil
}

type authConfig struct {
	// The username to use with basic authentication. This option is required if
	// auth.basic.password contains a value. If both auth.basic.username and auth.basic.password
	// are empty basic authentication is disabled.
	Username string `json:"basic.username"`
	// The password to use with basic authentication. This option is required if
	// auth.basic.username contains a value. If both auth.basic.username and auth.basic.password
	// are empty basic authentication is disabled.
	Password string `json:"basic.password"`
}

func (c *authConfig) validate() error {
	switch {
	case c.Username == "" && c.Password == "":
		// no basic auth set
		return nil
	case c.Username == "":
		return cerrors.Errorf("specify a username to enable basic auth or remove field password")
	case c.Password == "":
		return cerrors.Errorf("specify a password to enable basic auth or remove field username")
	}

	return nil
}

type clientCert struct {
	// The path to a file containing a PEM encoded certificate. This option is required
	// if tls.client.key contains a value. If both tls.client.cert and tls.client.key are empty
	// TLS is disabled.
	Cert string `json:"cert"`
	// The path to a file containing a PEM encoded private key. This option is required
	// if tls.client.cert contains a value. If both tls.client.cert and tls.client.key are empty
	// TLS is disabled.
	Key string `json:"key"`
}

type tlsConfig struct {
	// The path to a file containing PEM encoded CA certificates. If this option is empty,
	// Conduit falls back to using the host's root CA set.
	CACert string `json:"ca.cert"`

	Client clientCert `json:"client"`

	tlsClientCert *tls.Certificate
	tlsCACert     *x509.CertPool
}

func (c *tlsConfig) parse() error {
	if c.Client.Cert == "" && c.Client.Key == "" && c.CACert == "" {
		// no tls config set
		return nil
	} else if c.Client.Cert == "" || c.Client.Key == "" {
		// we are missing some configuration fields
		errs := []error{cerrors.New("invalid TLS config")}
		if c.Client.Cert == "" {
			errs = append(errs, cerrors.New("missing field: tls.client.cert"))
		}
		if c.Client.Key == "" {
			errs = append(errs, cerrors.New("missing field: tls.client.key"))
		}
		// CA cert is optional, we don't check if it's missing
		return cerrors.Join(errs...)
	}

	clientCert, err := tls.LoadX509KeyPair(c.Client.Cert, c.Client.Key)
	if err != nil {
		return fmt.Errorf("failed to load client certificate: %w", err)
	}

	c.tlsClientCert = &clientCert

	if c.CACert != "" {
		// load custom CA cert
		caCert, err := os.ReadFile(c.CACert)
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
