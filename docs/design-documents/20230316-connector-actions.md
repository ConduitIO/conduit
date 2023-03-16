# Connector Actions

## Introduction

TBD

### Background

TBD

### Goals

TBD

## Implementation options

### Option 1

In the connector protocol we add one method that accepts an action name and a
map of arguments. The list of available actions are exposed by the connector
through its specification. Each action also defines the parameters it expects.
This makes connector actions discoverable and allows us to display action
triggers in the UI.

```protobuf
service SourcePlugin/DestinationPlugin {
  // Action triggers an action in the plugin.
  rpc Action(Source.Action.Request) returns (Source.Action.Response);
}

message Source/Destination {
  message Action {
    message Request {
      // Name contains the name of the triggered action.
      string name = 1;
      // Contains the action arguments.
      map<string, string> args = 2;
    }
    message Response {
      string response = 1; // See Question 3 - should there even be a response? If so, is string the right type?
    }
  }
}


message Specifier {
  message Specify {
    /* ... */
    message Response {
      /* ... */

      // A map that describes actions that can be triggered for the destination.
      // The key of the map is the action name.
      map<string, Specifier.Action> destination_actions = 8;
      // A map that describes actions that can be triggered for the source.
      // The key of the map is the action name.
      map<string, Specifier.Action> source_actions = 9;
    }
  }

  // Action describes an action known to the plugin.
  message Action {
    // Description documents the action, what it does and why it is useful.
    string description = 1;
    // A map that describes parameters expected to be included when the action
    // is triggered.
    map<string, Specifier.Parameter> params = 2;
  }

  /* ... */
}
```

TBD:

- Figure out how to allow the connector developer to expose custom actions.
- Figure out the API endpoint that lets a user trigger custom actions.

#### Pros

- Actions are discoverable.
- Backwards compatibility is maintained (a connector that doesn't describe any
  custom actions is still a valid connector).

#### Cons

TBD

## Questions

- **Can actions be triggered only while the connector is running? (i.e.
  configured and alive, either producing or receiving records)**
- **What happens if an action fails?**
- **Should an action be able to return a result?**
