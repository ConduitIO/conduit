# Conduit Processor Template

Conduit processor for Cohere's models.

## Functionality

Provides Cohere processors for command, embed and rerank models.

### Command Processor Configuration

| name                     | description                              | required | default value |
|--------------------------|------------------------------------------|----------|---------------|
| `model` | Model is one of the Cohere model (command,embed,rerank) | false     | "command"            |
| `apiKey` | APIKey is apikey for Cohere api calls. | true     |             |
| `prompt` | Prompt is the preset prompt. | true     |             |
| `request.body` | RequestBodyRef specifies the api request field. | false     |     `.Payload.After`        |
| `response.body` | Specifies in which field should the response body be saved. | false     |     `.Payload.After`        |
| `backoffRetry.count` |Maximum number of retries for an individual record when backing off following an error. | false     |        `0`     |
| `backoffRetry.factor` | The multiplying factor for each increment step. | false     |     `2`        |
| `backoffRetry.min` | The minimum waiting time before retrying. | false     |     `100ms`        |
| `backoffRetry.max` | The maximum waiting time before retrying. | false     |    `5s`         |

### Embed Processor Configuration

| name                     | description                              | required | default value |
|--------------------------|------------------------------------------|----------|---------------|
| `model` | Model is one of the Cohere embed models | false     | `embed-english-v2.0`            |
| `apiKey` | APIKey is apikey for Cohere api calls. | true     |             |
| `inputType` | InputType specifies the type of input passed to the model. Required for embed models v3 and higher. [See more.](https://docs.cohere.com/reference/embed#request.body.input_type) | false     |             |
| `backoffRetry.count` |Maximum number of retries for an individual record when backing off following an error. | false     |        `0`     |
| `backoffRetry.factor` | The multiplying factor for each increment step. | false     |     `2`        |
| `backoffRetry.min` | The minimum waiting time before retrying. | false     |     `100ms`        |
| `backoffRetry.max` | The maximum waiting time before retrying. | false     |    `5s`         |
| `inputField` | InputField specifies the field from which the request body should be created. | false     |    `.Payload.After`         |

## References

- Cohere docs: <https://docs.cohere.com/docs/foundation-models>
- Command model: <https://docs.cohere.com/docs/introduction-to-text-generation-at-cohere>
- Embed model: <https://docs.cohere.com/docs/cohere-embed>
- Rerank model: <https://docs.cohere.com/docs/rerank-2>
- APIs: <https://docs.cohere.com/v1/reference>
