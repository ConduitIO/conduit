# Conduit Processor for Text Generation

[Conduit](https://conduit.io) processor for text generation.

## Functionality

This processor uses the specified OpenAI model to transform records based on a given prompt. It takes the content from a specified field in the record, sends it to OpenAI with a given developer message, and replaces the original content with the generated response.

### Processor Configuration

| name                    | description                                              | required | default value    |
| ----------------------- | -------------------------------------------------------- | -------- | ---------------- |
| `field`                 | Reference to the field to process                        | false    | ".Payload.After" |
| `api_key`               | OpenAI API key                                           | true     | ""               |
| `developer_message`     | System message that guides the model's behavior          | true     | ""               |
| `strict_output`         | Whether to enforce strict output format                  | false    | false            |
| `model`                 | OpenAI model to use (e.g., gpt-4o-mini)                  | true     | ""               |
| `max_tokens`            | Maximum number of tokens to generate                     | false    | ""               |
| `max_completion_tokens` | Maximum number of tokens for completion                  | false    | ""               |
| `temperature`           | Controls randomness (0-2, lower is more deterministic)   | false    | ""               |
| `top_p`                 | Controls diversity via nucleus sampling                  | false    | ""               |
| `n`                     | Number of completions to generate                        | false    | ""               |
| `stop`                  | Sequences where the API will stop generating             | false    | ""               |
| `presence_penalty`      | Penalizes new tokens based on presence in text           | false    | ""               |
| `seed`                  | Seed for deterministic results                           | false    | ""               |
| `frequency_penalty`     | Penalizes new tokens based on frequency in text          | false    | ""               |
| `logit_bias`            | Modifies likelihood of specified tokens appearing        | false    | ""               |
| `log_probs`             | Whether to return log probabilities of output tokens     | false    | ""               |
| `top_log_probs`         | Number of most likely tokens to return probabilities for | false    | ""               |
| `user`                  | User identifier for OpenAI API                           | false    | ""               |
| `store`                 | Whether to store the conversation in OpenAI              | false    | ""               |
| `reasoning_effort`      | Controls the amount of reasoning in the response         | false    | ""               |
| `metadata`              | Additional metadata to include with the request          | false    | ""               |

## Known Issues & Limitations

- API rate limits may affect processing speed for large volumes of records
- Token limits may truncate very long inputs or outputs
- Requires an active internet connection to function
