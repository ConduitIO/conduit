# Built-in config validation

This design document discusses the best approach for implementing built-in config validations.

## The problem

Config validations are not enforced in Conduit or the SDK, and developers have the freedom of either validating the
configuration for their connectors or not. So there’s a chance that some configurations could be marked as mandatory,
but not enforced in any way.

## What we currently do

We have a `validate` endpoint for Conduit, which users can use to make sure that the connector configurations are valid,
without actually creating the connector.

**Cons**:

- It’s just an option for the user to use this.
- It calls the `Configure` function, which is written by the developer, so still not guaranteed to enforce the validations.

We also have this proto message:

```protobuf
message PluginSpecifications {
  message Parameter {
    // Validation to be made on the parameter.
    message Validation{
      enum Type {
        TYPE_UNSPECIFIED = 0;
        // Parameter must be present.
        TYPE_REQUIRED = 1;
        // Parameter must be greater than {value}.
        TYPE_GREATER_THAN = 2;
        // Parameter must be less than {value}.
        TYPE_LESS_THAN = 3;
        // Parameter must be included in the comma separated list {value}.
        TYPE_INCLUSION = 4;
        // Parameter must not be included in the comma separated list {value}.
        TYPE_EXCLUSION = 5;
        // Parameter must match the regex {value}.
        TYPE_REGEX = 6;
      }

      Type type = 1;
      // The value to be compared with the parameter,
      // or a comma separated list in case of Validation.TYPE_INCLUSION or Validation.TYPE_EXCLUSION.
      string value = 2;
    }

    // Type shows the parameter type.
    enum Type {
      TYPE_UNSPECIFIED = 0;
      // Parameter is a string.
      TYPE_STRING = 1;
       // Parameter is an integer.
       TYPE_INT = 2;
       // Parameter is a float.
       TYPE_FLOAT = 3;
      // Parameter is a boolean.
      TYPE_BOOL = 3;
      // Parameter is a file.
      TYPE_FILE = 4;
      // Parameter is a duration.
      TYPE_DURATION = 5;
    }

    string description = 1;
    string default = 2;
    Type type = 3;
    repeated Validation validations = 4;
  }

  string name = 1;
  string summary = 2;
  string description = 3;
  string version = 4;
  string author = 5;
  map<string, Parameter> destination_params = 6;
  map<string, Parameter> source_params = 7;
}
```

For now, this is only used when listing the plugins in Conduit. All these validation options are not yet implemented and
are not exposed by the SDK.

## Scope

Implementing the validation options provided by the proto file. So, giving the developer the option to specify
validations for each parameter and Conduit will make sure to run the validations.

Validating the type of the parameter, we have 6 types supported in the proto design {string, int, float, bool, file, and duration}

Validate that the config doesn't contain a parameter that is not defined in the specifications, which will help detect
a typo in a pipeline configuration file, or an extra configuration that does not exist.

Providing a utility function to generate the config map for the `Parameters` function from a config struct. This is not
mandatory for the scope of this feature, but it would be a nice to have and would make the developing experience for connectors easier.

## Questions

**Q**: Should we implement the validations from the SDK or Conduit side?

**A**: If we implement it from Conduit side, this means we can add more validations in the future without changing the SDK.
However, implementing it from the SDK side means that all the validations will happen on the connector side, using the
Config function, and the developer will still have the ability to add custom validations for some specific cases that
the SDK wouldn't cover. So we decided on implementing validating from the SDK side.

**Q**: Should generating the Configurations from a Go struct be in the scope of this feature?

**A**: We decided to add the generation as a subtask for now, and see how much time left we have for it. We agreed that this
would be super helpful for developers and easier to use, and will continue adding to the feature in the future.

**Q**: How should the UI execute the validations?

**A**: The UI will execute the builtlin validations for each configuration while the user is typing (validations
are in the same struct that has the list of the fields). When the user submits the form, the UI will try and
create the connector, which will execute both the builtin and the custom validation. Finally, the UI will show an
error for the user if an error occurs while creating the connector.

## Implementation

Implementing this feature consists of four main steps:

1. Adjust the connector protocol by adding validations for parameters, this change should be done in a backwards
   compatible way, so the old `required` field needs to be parsed into a validation.

2. Adjust the connector SDK to give developers the ability to specify validations needed for each parameter. (manually)

Params should look something like:

```go
SourceParams: []sdk.Parameter{
    {
      Name: "param",
      Type: sdk.ParameterTypeInt,
      Validations: []sdk.Validation{
        sdk.ValidationRequired{},
        sdk.ValidationLessThan{Value:8},
      }
    }
  }
```

3. Provide a function that takes the parameters' validations and validates them in the `Configure` function on the SDK.
4. Generate connector configurations from a Go struct, which will give the ability to generate the connector's
   configurations from a Go struct, the struct would have field tags that specify validations, default value, and if
   a parameter is required.

   example:

```go
type Config struct {
	param1 string `validate:"greater-than:0" required:"true"`
	param2 string `validate:"less-than:100" default:"10"`
}
```
