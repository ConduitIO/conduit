export default function (server) {
  // const genericConnectorBlueprint = [
  //   {
  //     id: "connection:url",
  //     label: "Connection URL",
  //     placeholder: "Enter Connection URL",
  //     type: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },

  //   {
  //     id: "connection:user",
  //     label: "Connection User",
  //     placeholder: "Enter Connection User",
  //     type: "string",
  //     validationType: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },

  //   {
  //     id: "connection:password",
  //     label: "Connection Password",
  //     placeholder: "Enter Connection Password",
  //     type: "string",
  //     validationType: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },

  //   {
  //     id: "optional:string",
  //     label: "Optional String",
  //     placeholder: "Enter an optional string",
  //     type: "string",
  //     validationType: "string",
  //     validations: [],
  //   },

  //   {
  //     id: "optional:integer",
  //     label: "Optional Integer",
  //     placeholder: "Enter an optional integer",
  //     type: "int",
  //     validations: [],
  //   },

  //   {
  //     id: "optional:long",
  //     label: "Optional Long",
  //     placeholder: "Enter an optional long",
  //     type: "long",
  //     validations: [],
  //   },

  //   {
  //     id: "optional:password",
  //     label: "Optional password",
  //     placeholder: "Enter an optional password",
  //     type: "password",
  //     validations: [],
  //   },

  //   {
  //     id: "optional:boolean",
  //     label: "Optional Boolean",
  //     placeholder: null,
  //     type: "boolean",
  //     validations: [],
  //   },
  // ];

  // const s3ConnectorPluginBlueprint = [
  //   {
  //     id: "format.class",
  //     label: "Format Class",
  //     placeholder: "Select a Format Class",
  //     type: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },

  //       {
  //         type: "inclusion",
  //         options: {
  //           list: ["Avro", "Byte Array", "JSON", "Parquet"],
  //         },
  //         params: [],
  //       },
  //     ],
  //   },

  //   {
  //     id: "integer.with.validations",
  //     label: "Integer with validations",
  //     placeholder: null,
  //     type: "int",
  //     validations: [
  //       {
  //         type: "number",
  //         options: {
  //           gt: 0,
  //           lt: 10,
  //           allowBlank: true,
  //         },
  //         params: ["must be between 0 and 10"],
  //       },
  //     ],
  //   },

  //   {
  //     id: "access.key.id",
  //     label: "Access Key ID",
  //     placeholder: "Enter Access Key ID",
  //     type: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },

  //   {
  //     id: "access.key.secret",
  //     label: "Access Key Secret",
  //     placeholder: "Enter Access Key Secret",
  //     type: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },
  //   {
  //     id: "s3.bucket.name",
  //     label: "S3 Bucket Name",
  //     placeholder: "Enter S3 bucket name",
  //     type: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },
  // ];

  // server.create("connector-plugin", {
  //   name: "File Connector",
  //   connectorType: "source",
  //   pluginPath: "pkg/plugins/file/file",
  //   blueprint: [
  //     {
  //       id: "path",
  //       label: "File Path",
  //       placeholder: "Enter path to file",
  //       type: "string",
  //       validations: [
  //         {
  //           type: "required",
  //           params: ["this field is required"],
  //         },
  //       ],
  //     },
  //   ],
  // });

  // server.create("connector-plugin", {
  //   name: "File Connector",
  //   connectorType: "destination",
  //   pluginPath: "pkg/plugins/file/file",
  //   blueprint: [
  //     {
  //       id: "path",
  //       label: "File Path",
  //       placeholder: "Enter path to file",
  //       type: "string",
  //       validations: [
  //         {
  //           type: "required",
  //           params: ["this field is required"],
  //         },
  //       ],
  //     },
  //   ],
  // });

  // const mongoDBConnectorPlugin = server.create('connector-plugin', { name: 'MongoDB', pluginType: 'source', blueprint: genericConnectorBlueprint });
  // const postgresConnectorPlugin = server.create('connector-plugin', { name: 'Postgres', pluginType: 'all', blueprint: genericConnectorBlueprint });
  // const S3ConnectorPlugin = server.create('connector-plugin', { name: 'S3', pluginType: 'destination', blueprint: s3ConnectorPluginBlueprint });
  // const redshiftConnectorPlugin = server.create('connector-plugin', { name: 'Redshift', pluginType: 'destination', blueprint: genericConnectorBlueprint });
  // const sparkConnectorPlugin = server.create('connector-plugin', { name: 'Spark', pluginType: 'destination', blueprint: genericConnectorBlueprint });

  // const pipeline = server.create("pipeline", {
  //   name: "My Pipeline w/ connectors",
  //   tags: ["attack", "colossal", "armored"],
  //   favorite: true,
  //   pipelineState: "running",
  //   sourceCount: 2,
  //   destinationCount: 2,
  // });

  // server.createList("pipeline", 11, {
  //   name: "My Pipeline w/ zero state",
  //   tags: ["beast", "jaw", "warhammer", "cart"],
  //   favorite: false,
  //   pipelineState: "paused",
  //   sourceCount: 0,
  //   destinationCount: 0,
  // });

  // // Transforms
  // const maskFieldFields = [
  //   {
  //     id: "field",
  //     label: "Field Name",
  //     placeholder: "Enter field(s) to mask",
  //     type: "string",
  //     validationType: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },
  // ];

  // const flattenFields = [
  //   {
  //     id: "delimiter",
  //     label: "Delimiter",
  //     placeholder: "Enter delimiter to flatten with",
  //     type: "string",
  //     validationType: "string",
  //     validations: [
  //       {
  //         type: "required",
  //         params: ["this field is required"],
  //       },
  //     ],
  //   },
  // ];

  // const insertFieldFields = [
  //   {
  //     id: "staticField",
  //     label: "Static Field",
  //     placeholder: "Enter static field name",
  //     type: "string",
  //     validationType: "string",
  //     validations: [],
  //   },

  //   {
  //     id: "staticValue",
  //     label: "Static Field Value",
  //     placeholder: "Enter static field value",
  //     type: "string",
  //     validationType: "string",
  //     validations: [],
  //   },
  // ];

  // const customFields = [
  //   {
  //     id: "foo",
  //     label: "Custom Foo",
  //     placeholder: "Enter custom value to foo with",
  //     type: "string",
  //     validationType: "string",
  //     validations: [],
  //   },
  // ];

  // server.create("transform", {
  //   name: "MaskField",
  //   type: "io.meroxa.conduit.transforms.MaskField",
  //   isCustom: false,
  //   blueprint: maskFieldFields,
  //   description:
  //     "Replace field with a valid null value for the type or custom replacement.",
  // });
  // const customTransformA = server.create("transform", {
  //   name: "Flatten",
  //   type: "io.meroxa.conduit.transforms.Flatten",
  //   isCustom: false,
  //   blueprint: flattenFields,
  //   description: "Flatten a nested field using a delimiter.",
  // });
  // const customTransformB = server.create("transform", {
  //   name: "InsertField",
  //   type: "io.meroxa.conduit.transforms.InsertField",
  //   isCustom: false,
  //   blueprint: insertFieldFields,
  //   description: "Add a field using static data or record meta data.",
  // });
  // const customTransformC = server.create("transform", {
  //   name: "Custom Foo",
  //   type: "io.notmeroxa.conduit.transforms.CustomFoo",
  //   isCustom: true,
  //   blueprint: customFields,
  //   description: "Custom foo description goes here",
  // });

  // const attachedTransforms = [
  //   {
  //     name: "myConnectorTransform1",
  //     ordinal: 0,
  //     type: customTransformA.type,
  //     config: {
  //       delimiter: ".",
  //     },
  //   },
  //   {
  //     name: "myConnectorTransform2",
  //     ordinal: 1,
  //     type: customTransformB.type,
  //     config: {
  //       staticField: "newField",
  //       staticValue: "newValue",
  //     },
  //   },
  //   {
  //     name: "myConnectorTransform3",
  //     ordinal: 2,
  //     type: customTransformC.type,
  //     config: {
  //       foo: "bar",
  //     },
  //   },
  // ];

  // const genericConnectorConfigV2 = {
  //   "connection:url": "foo://bar",
  //   "connection:user": "james",
  //   "connection:password": "supersecurepassword",
  // };

  // const s3ConnectorConfigV2 = {
  //   "access:key:id": "123456",
  //   "access:key:secret": "supersecurepassword",
  //   bucket: "s3bucket",
  // };

  // server.create("connector", {
  //   name: "Connector A",
  //   connectedType: "source",
  //   connectorPlugin: mongoDBConnectorPlugin,
  //   transforms: attachedTransforms,
  //   connectorState: "running",
  //   config: genericConnectorConfigV2,
  //   pipeline,
  // });

  // server.create("connector", {
  //   name: "Connector B",
  //   connectedType: "source",
  //   connectorPlugin: postgresConnectorPlugin,
  //   transforms: [],
  //   connectorState: "degraded",
  //   config: genericConnectorConfigV2,
  //   pipeline,
  // });

  // server.create("connector", {
  //   name: "Connector C",
  //   connectedType: "destination",
  //   connectorPlugin: S3ConnectorPlugin,
  //   transforms: [],
  //   connectorState: "running",
  //   config: s3ConnectorConfigV2,
  //   pipeline,
  // });

  // server.create("connector", {
  //   name: "Connector D",
  //   connectedType: "destination",
  //   connectorPlugin: redshiftConnectorPlugin,
  //   transforms: [],
  //   connectorState: "paused",
  //   config: genericConnectorConfigV2,
  //   pipeline,
  // });

  // server.create("configuration", {
  //   connectorDirectory: "/foo/bar/bar/connectors",
  //   transformDirectory: "/foo/bar/baz/transforms",
  //   conduitPort: "4200",
  //   logRetention: "30 days",
  //   metricsRetention: "30 days",
  //   metricsExportEndpoint: "/metrics",
  // });

  // ##### v1 Scenario ######
  server.createList('pipeline', 11, 'withFileConnectors');
}
