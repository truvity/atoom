# The AppConfig driver

How the [source port](../../docs/source-port.md) maps to AWS AppConfig. The driver reads through the AppConfig **data plane** (the service `appconfigdata`), the API that AppConfig gives to the programs that consume a configuration; it never calls the control plane, so it cannot create, deploy or change a configuration.

## The mapping

| Port | AppConfig |
| --- | --- |
| The source | one application and one environment of it, named by the host (`Options.Application`, `Options.Environment`: names or ids). The driver gives the words no meaning ([record 0021](../../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)) |
| A name | a configuration profile: the name itself, or the identifier that `Options.Profiles` maps it to (an id, or a name that is not kebab-case). A name is kebab-case, 1 to 63 characters, as for the file driver, so one name reads the same document from either |
| `Read` | `StartConfigurationSession` at the first read of a name, then `GetLatestConfiguration` with the token of the last answer, at most once in each poll interval |
| The bytes | the configuration deployed to the environment, exactly as AppConfig returns it |
| The version label | the SHA-384 of the bytes, in hex. AppConfig's `VersionLabel` is not used: it is optional, set by people, and absent for a configuration that is not a hosted one |
| `if_none_match` | compared with the label the driver holds, as for every driver; AppConfig's own "no change" (an empty answer to a session that already has the latest) keeps the bytes and the label of the last poll |
| `Capabilities` | `POLL` |
| `poll_after` | the time until the next poll of the name: AppConfig's `NextPollIntervalInSeconds` after a poll, what is left of it between two, and never less than a second |
| `Watch` | `unimplemented`, reason `rule` |

## Refusals

The field is always the request's `name`.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `schema` | a name that is not kebab-case; AppConfig is not called |
| `not_found` | `absent` | AppConfig answers `ResourceNotFoundException` (no such application, environment or profile), or the first answer of a session is empty (a profile with nothing deployed in the environment) |
| `failed_precondition` | `bound` | a configuration over 4 MiB (`DocumentMax`, the file driver's limit; AppConfig's own store holds at most 2 MB, a configuration in S3 may be larger) |
| `unavailable` | `engine` | any other answer of AppConfig (throttling, a server error, a session refused twice), or none; the message is the reason only, and AppConfig's text goes to the operator's log |

After a refusal the name's session and what it brought are dropped: the next read starts a new session and gets the whole document again, so a document that was once refused is never served from a session that already "has" it.

## Why AppConfig first, and SSM after

session reads its site file and its signed policy bundle through the source port on Lambda (its step 5); SSM Parameter Store and AppConfig were both planned ([record 0010](../../../docs/decisions/0010-distribution-per-driver-and-lambda.md)). AppConfig is built first (the owner's decision D71 A, 2026-11-25, which these reasons had pointed to), because:

- **Size.** A signed document is a JWS of up to 3 MiB ([record 0019](../../../docs/decisions/0019-the-policy-bundle-and-its-loader.md)), and base64 makes a payload a third larger. An SSM parameter holds 4 KB (standard) or 8 KB (advanced): a site file of a dozen routes with its pinned key, or a bundle with a schema, does not fit. AppConfig's own store holds 2 MB.
- **Refresh.** The port already names AppConfig as its `POLL` driver (`source.proto`, [the source port](../../docs/source-port.md#drivers)): AppConfig says how long to wait, and a function sees a new deployment within one interval, with no new version of the function. SSM has no change notification that reaches a running process; a reader would poll `GetParameter` on its own clock.
- **Deployment.** AppConfig deploys a configuration by a strategy, checks it with validators and rolls it back on an alarm; a document a whole site depends on deserves that. SSM writes a parameter in one step.
- **IAM and cost.** Both need one or two read actions on one resource (`appconfig:StartConfigurationSession` and `GetLatestConfiguration`, or `ssm:GetParameter` and `kms:Decrypt` for a SecureString). AppConfig prices each `GetLatestConfiguration` call and each configuration received; at one poll a minute for each document and instance, that is small. An advanced SSM parameter is priced each month and each API interaction.

`source/ssm` is still planned (step B9): it fits a **secret** source, a SecureString of a few hundred bytes read with `kms:Decrypt`, and small values that change by a new deployment. It reports `NONE` (or `POLL` on its own clock) and its label is the parameter's version.

## What the fake cannot judge

The tests run against `internal/appconfigfake`, an HTTP server that speaks the two operations' REST-JSON protocol, hands out one-use tokens and answers empty when a session already has the latest content. It is not AppConfig. Not checked by it, and so to be confirmed in a real account before a site depends on it:

- the exact answer to a profile that exists but has nothing deployed in the environment (the driver takes an empty first answer, or `ResourceNotFoundException`, as absent);
- the error type of a token that is reused or older than 24 hours (the driver takes `BadRequestException` as such, and tries one new session);
- the quotas and the throttling of `StartConfigurationSession` at the cold start of many function instances at once (each instance starts one session for each name it reads);
- the latency of a poll from a function.
