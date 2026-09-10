# SOTNPresetAPI

A Go API for sharing SOTNPresetGenerator options and exported presets, browsing the community catalog, and voting. AWS SAM provisions API Gateway HTTP API, an ARM64 Lambda function, DynamoDB, and Cognito accounts.

Reads are public. Submissions and votes require a Cognito **access token**. Each account has one vote per item; setting the same vote twice does not change the total. Changing or removing a vote updates the totals transactionally.

The integration contract is [openai.json](openai.json), a standard OpenAPI 3.0.3 document with all nine operations, request and response schemas, pagination, voting examples, and authentication requirements. Import it into Swagger UI, Postman, or an OpenAPI client generator. It includes a localhost server and an AWS placeholder; replace the latter with the deployed stack's `ApiUrl`. Production writes use bearer authentication; local writes use `X-Dev-User` instead.

Ready-to-use [Bruno collections](bruno/README.md) cover every API route and Cognito login/token refresh. They include the deployed AWS environment and a local API environment, with passwords supplied through local secret variables.

## Endpoints

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Process health; does not probe the database |
| GET | `/v1/options` | List options |
| POST | `/v1/options` | Submit an option |
| GET | `/v1/options/{id}` | Get one option |
| PUT | `/v1/options/{id}/vote` | Set or remove your vote |
| GET | `/v1/presets` | List presets |
| POST | `/v1/presets` | Submit an exported preset |
| GET | `/v1/presets/{id}` | Get one preset |
| PUT | `/v1/presets/{id}/vote` | Set or remove your vote |

List endpoints accept `limit` (1–50, default 20) and an opaque `cursor`. Responses contain `items` and, when another page may exist, `nextCursor`. Follow cursors until one is absent to retrieve the whole catalog; an empty page alone does not indicate completion. Ordering is by immutable catalog ID, not score or creation date. Pagination is not a snapshot when submissions occur concurrently.

```json
{
  "items": [
    {
      "id": "0123456789abcdef0123456789abcdef",
      "kind": "options",
      "createdBy": "cognito-user-sub",
      "createdAt": "2026-09-09T20:00:00Z",
      "upvotes": 2,
      "downvotes": 1,
      "score": 1,
      "data": {
        "comment": "Library shortcut",
        "description": "Enable the library shortcut.",
        "category": "gameplay",
        "type": "string",
        "value": "{\"libraryShortcut\":true}",
        "address": null,
        "gameInit": false,
        "statEdit": false,
        "rawJson": true,
        "additionalWrites": []
      }
    }
  ]
}
```

Create and get endpoints return a single item in this format. Creation returns `201` and a `Location` header. Submit the option or preset directly as the request body, without a `data` wrapper. Each POST creates a separate submission; POST retries are not deduplicated.

### Options

The input matches the generator's `CreateOptionInput`: `comment`, `category`, `type`, and `value` are required. Supported categories are `world`, `items`, `challenge`, `relics`, and `gameplay`; types are `char`, `short`, `word`, `long`, and `string`. `value` is a string, including for numeric write values.

Optional fields are `description`, `address`, `gameInit`, `statEdit`, `rawJson`, and `additionalWrites`. A raw JSON option's `value` must encode an object. Each additional write must be an object. The API validates the submission structure; the randomizer remains responsible for game addresses, write semantics, and preset validity. Requests and normalized JSON are limited to 128 KiB.

Server IDs, authorship, and vote totals cannot be supplied by clients. Unknown option fields are rejected. See [examples/option.json](examples/option.json).

### Presets and generator integration

Submit the generator's **exported preset JSON**, with nonblank `metadata.id` and `metadata.name`. Custom randomizer settings, explicit `false` values, and JSON number precision are preserved. `metadata.author` is display metadata; the authenticated submitter is recorded independently as `createdBy`. See [examples/preset.json](examples/preset.json).

The API assigns a catalog ID independently of `metadata.id`. Multiple submissions may share a preset metadata ID. A generator editor draft containing local `optionIds` is not an exported preset; export it before sharing.

The generator currently uses numeric SQLite option IDs. API IDs are 32-character hexadecimal strings. To import a shared option, pass `item.data` to the existing option creation flow and keep a separate mapping from catalog ID to the resulting local ID for voting. Do not replace local numeric IDs with catalog IDs. The `readOnly` flag is a local catalog concern and is not a submission field.

[examples/client.mjs](examples/client.mjs) provides an Electron main-process client with `create`, `get`, `vote`, and `listAll` methods for both collections:

```javascript
import { createCatalogClient } from './client.mjs';

const catalog = createCatalogClient(apiUrl, async () => accessToken);
const sharedOption = await catalog.options.create(createOptionInput);
const sharedPreset = await catalog.presets.create(JSON.parse(exportedPresetJson));
const allPresets = await catalog.presets.listAll();
await catalog.presets.vote(sharedPreset.id, 1);
```

The generator's UI and authentication flow have not been modified in this repository.

### Voting

Send `PUT /v1/{options|presets}/{id}/vote` with:

```json
{"value": 1}
```

Use `1` to upvote, `-1` to downvote, or `0` to remove the vote. The response contains the item and current aggregate totals. The voter is always the verified Cognito subject, never a body or header user ID. Votes on nonexistent items return `404` without creating an orphan vote. Transaction conflicts retry internally; a remaining conflict returns `409`, which the client can retry. Totals may include other votes committed before the response was read.

The application returns errors as `{"error":{"code":"invalid_request","message":"..."}}`, with statuses `400`, `401`, `404`, `405`, `409`, `413`, `415`, or `500`. API Gateway can reject authentication or throttled requests before Lambda runs, using its own response format.

## Local development

Install Go 1.26 or newer and Docker Compose. From this directory:

```sh
docker compose up -d
go run ./cmd/local
```

The local runner creates its table automatically and listens on `http://127.0.0.1:8080`. Docker stores the database in a named volume, so it survives ordinary container restarts. `DYNAMODB_ENDPOINT` optionally selects another local HTTP endpoint. This runner only accepts a loopback database endpoint and uses dummy credentials.

For local writes only, send `X-Dev-User` to choose a test identity. The deployed Lambda has no development-authentication mode.

```sh
curl http://127.0.0.1:8080/v1/presets
curl -X POST http://127.0.0.1:8080/v1/options \
  -H 'Content-Type: application/json' -H 'X-Dev-User: alice' \
  --data-binary @examples/option.json
curl -X PUT http://127.0.0.1:8080/v1/options/REPLACE_WITH_CATALOG_ID/vote \
  -H 'Content-Type: application/json' -H 'X-Dev-User: alice' \
  -d '{"value":1}'
```

PowerShell example:

```powershell
$base = 'http://127.0.0.1:8080'
$headers = @{ 'X-Dev-User' = 'alice' }
$item = Invoke-RestMethod "$base/v1/options" -Method Post -Headers $headers `
  -ContentType 'application/json' -Body (Get-Content examples/option.json -Raw)
Invoke-RestMethod "$base/v1/options/$($item.id)/vote" -Method Put -Headers $headers `
  -ContentType 'application/json' -Body '{"value":1}'
```

## Deploy to AWS

Install the AWS CLI and AWS SAM CLI, and configure AWS credentials for the target account. This repository does not contain credentials or deploy automatically.

```sh
sam validate --lint
sam build
sam deploy --guided
```

Choose a stack name such as `sotn-preset-api`, a region such as `us-west-2`, and allow SAM to create the function's IAM role. Public read routes intentionally have no authorizer. SAM builds `cmd/api` as the Linux ARM64 `bootstrap` executable with the `provided.al2023` runtime. Stack outputs provide `ApiUrl`, `UserPoolId`, `UserPoolClientId`, `Region`, and `TableName`.

For a subsequent release, use `sam build` and `sam deploy`. DynamoDB and the user pool are retained when the stack is deleted or replaced; retained resources must be managed separately. The table uses on-demand billing, encryption, and point-in-time recovery. API Gateway applies a per-route rate of 20 requests/second and burst of 40. This is a small community catalog design: all options share one partition key and all presets another; high traffic may require a different partition/index layout.

### Accounts and access tokens

The stack creates a username-and-password Cognito user pool and a public app client with **no client secret**, suitable for the desktop app. Register users with a unique username and password using Cognito `SignUp`; no email address, phone number, or confirmation code is required. A pre-sign-up Lambda trigger confirms these accounts automatically. Sign in using `USER_SRP_AUTH` or `USER_PASSWORD_AUTH` through a Cognito SDK. The returned **AccessToken** is sent as `Authorization: Bearer <token>` on submissions and votes. Access tokens last one hour; use the refresh token with `REFRESH_TOKEN_AUTH` to renew them without prompting the user to sign in again. Refresh tokens use Cognito's maximum lifetime of 3,650 days (10 years) and should be kept in OS-protected credential storage.

Cognito doesn't support a literally permanent session. A user must sign in again when the 10-year refresh token expires, when the token is revoked, after an administrator signs the user out globally, or when the account is disabled or deleted. The desktop client must persist the refresh token and renew the access token before or after its one-hour expiration for the long-lived login to work.

API Gateway verifies the issuer, client audience, expiry, and `aws.cognito.signin.user.admin` scope. Lambda additionally requires `token_use=access` and a nonempty `sub` claim. ID tokens and caller-provided identity headers are not accepted. The Cognito methods above issue the required access-token scope. Because accounts have no email address or phone number, Cognito self-service password recovery is disabled; an administrator must reset a forgotten password with `AdminSetUserPassword` or an equivalent administrative workflow.

Changing from email sign-in to username sign-in requires a new Cognito user pool because AWS doesn't allow `UsernameAttributes` to be changed in place. The template uses the new logical resource ID `UsernameUsers` instead of `Users` so CloudFormation creates a new pool and replaces the app client. The old pool's `DeletionPolicy: Retain` preserves it and its users outside the stack, but those users aren't migrated automatically. After deployment, use the new `UserPoolId` and `UserPoolClientId` stack outputs and create new username-based accounts.

If an earlier deployment failed with `Updates are not allowed for property - UsernameAttributes` and the stack reached `UPDATE_ROLLBACK_COMPLETE`, run `sam build` and `sam deploy` again with this updated template. Review the change set for addition of `UsernameUsers`, removal of the retained `Users` resource, and replacement of `DesktopClient`. Keep the `UsernameUsers` logical ID for subsequent deployments. You do not need to delete the stack or its DynamoDB table.

After the replacement deployment succeeds, an old retained pool that is no longer needed can be deleted separately with `aws cognito-idp delete-user-pool --user-pool-id OLD_POOL_ID --region REGION`. This permanently deletes its accounts. Update `cognito_client_id` in both Bruno AWS environments to the new `UserPoolClientId` output and clear tokens from the old pool before signing in again.

AWS implementation references: [Go Lambda packaging](https://docs.aws.amazon.com/lambda/latest/dg/golang-package.html), [SAM JWT authorizers](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/serverless-controlling-access-to-apis-oauth2-authorizer.html), and [DynamoDB transactions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis.html).

## Verification

```sh
go test ./...
go vet ./...
```

To include real database integration tests, start DynamoDB Local and set `DYNAMODB_ENDPOINT=http://localhost:8000` before running `go test -count=1 ./...`. In PowerShell, use `$env:DYNAMODB_ENDPOINT = 'http://localhost:8000'`. Tests create and delete their own tables and refuse remote database endpoints.

The GitHub Actions workflow runs formatting checks, vet, race-enabled unit and DynamoDB integration tests, a Linux ARM64 build, and SAM validation/build. On Windows, `go test -race` additionally requires a supported C compiler. Integration tests cover persistence, collection isolation, pagination, duplicate submissions by ID, vote switching/removal, simultaneous voters, duplicate concurrent votes, and missing targets. Unit tests cover request validation, authentication boundaries, and error responses.

Code layout: `cmd/api` is the production Lambda; `cmd/local` is the development HTTP server; `internal/catalog` owns the API and validation; `internal/storage` owns DynamoDB; `internal/transport` adapts API Gateway events.
