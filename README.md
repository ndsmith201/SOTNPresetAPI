# SOTNPresetAPI

A Go API for sharing SOTNPresetGenerator options and exported presets, browsing the community catalog, voting, and publishing featured mods. AWS SAM provisions API Gateway HTTP API, an ARM64 Lambda function, DynamoDB, Cognito accounts, and private S3 storage for featured images and PPF files.

Reads are public. Submissions and votes require a Cognito **access token**. Publishing a featured mod additionally requires membership in the `featured-mod-publishers` Cognito group. Each account has one vote per item; setting the same vote twice does not change the total. Changing or removing a vote updates the totals transactionally.

The integration contract is [openai.json](openai.json), a standard OpenAPI 3.0.3 document with all twelve operations, request and response schemas, pagination, voting and featured-mod examples, and authentication requirements. Import it into Swagger UI, Postman, or an OpenAPI client generator. It includes a localhost server and an AWS placeholder; replace the latter with the deployed stack's `ApiUrl`. Production writes use bearer authentication; local writes use `X-Dev-User` instead.

Ready-to-use [Bruno collections](bruno/README.md) cover every API route and Cognito login/token refresh. They include the deployed AWS environment and a local API environment, with passwords supplied through local secret variables.

Catalog responses include an optional `createdByUsername`; the original `createdBy` remains the ownership identity. New options resolve their creator's Cognito username before insertion and store it on the option row. Option lists, detail reads, votes, and same-name updates reuse that saved value without querying Cognito. If the initial lookup fails or finds no username, creation returns `503 author_unavailable` before saving. Existing options without a username need the backfill below. Local mode continues using `X-Dev-User` as the displayable creator.

Preset responses still resolve names at read time with a 15-minute in-memory cache (one minute for missing users), at most four concurrent calls, and a three-second response budget. Lambda queries `ListUsers` by exact `sub`, requesting only that attribute alongside the username. Deploy the updated SAM template to supply `USER_POOL_ID` and pool-scoped `cognito-idp:ListUsers` permission.

## Backfill existing option authors

Requires Go and AWS credentials with `dynamodb:Query`, `cognito-idp:ListUsers`, and, for writes, `dynamodb:UpdateItem` on the target table/pool. Use the stack's `TableName` and `UserPoolId` outputs. Preview first:

```powershell
./scripts/backfill-option-authors.ps1 -TableName 'YOUR_TABLE' -UserPoolId 'YOUR_POOL' -Region us-east-1
```

Add `-Apply` to write missing usernames. Use `-Profile` for a named AWS profile. The equivalent cross-platform command is `go run ./cmd/backfill-option-authors -table YOUR_TABLE -user-pool YOUR_POOL -region us-east-1`, with `-apply` to write.

The script queries only the options partition, follows every page, and looks up each distinct missing author once per run. It updates only `createdByUsername`, leaving ownership, payloads, votes, and creation dates intact. Conditional writes skip deleted rows, changed owners, and names filled concurrently. Already populated names are left alone; rerunning is safe. Missing/deleted Cognito accounts remain unresolved and appear in the summary. Lookup or database errors return a nonzero exit code; rerun after fixing the reported issue. Deploy the API change before applying the backfill so newly created options also receive stored names.

## Endpoints

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Process health; does not probe the database |
| GET | `/v1/options` | List options |
| POST | `/v1/options` | Create an option or update a same-name option as its author |
| GET | `/v1/options/{id}` | Get one option |
| PUT | `/v1/options/{id}/vote` | Set or remove your vote |
| GET | `/v1/presets` | List presets |
| POST | `/v1/presets` | Create a preset or update a same-name preset as a listed author |
| GET | `/v1/presets/{id}` | Get one preset |
| PUT | `/v1/presets/{id}/vote` | Set or remove your vote |
| GET | `/v1/featured-mods` | Get the most recently published featured mod |
| POST | `/v1/featured-mods` | Publish a featured mod with an uploaded image and PPF (publishers only) |
| GET | `/v1/featured-mods/{id}/download` | Download a released featured mod’s PPF file |

Option and preset list endpoints accept `limit` (1–50, default 20) and an opaque `cursor`. Responses contain `items` and, when another page may exist, `nextCursor`. Follow cursors until one is absent to retrieve the whole catalog; an empty page alone does not indicate completion. Ordering is by immutable catalog ID, not score or creation date. Pagination is not a snapshot when submissions occur concurrently.

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
        "value": "{\"libraryShortcut\":true}",
        "gameInit": false,
        "itemInit": false,
        "mainBlock": false,
        "statEdit": false,
        "rawJson": true,
        "writes": []
      }
    }
  ]
}
```

Option and preset create and get endpoints return a single item in this format. Creation returns `201` and a `Location` header; updating an option or preset returns `200` with the updated item and existing location. Submit the option or preset directly as the request body, without a `data` wrapper. Do not automatically retry uncertain POSTs; refresh the catalog first.

### Options

The input uses `comment`, `category`, and a single ordered `writes` array. Each memory write contains its own `type`, string or numeric `value`, optional `address`, and optional `comment`; extra properties are preserved, including on the first write. Supported categories are `world`, `items`, `challenge`, `relics`, and `gameplay`; types are `char`, `short`, `word`, `long`, and `string`. Memory options require 1–257 writes.

Optional fields are `description`, `gameInit`, `itemInit`, `mainBlock`, `statEdit`, and `rawJson`. Set `itemInit: true` for writes placed in the generator's item-initialization block. It is preserved on publication, updates, and public reads; missing or null becomes `false`, including for legacy options. No database migration is required. See [examples/item-init-option.json](examples/item-init-option.json). JSON settings use `rawJson: true`, an empty `writes` array, and a top-level `value` string encoding a JSON object. Memory options have no top-level `type`, `value`, or `address`.

Set `mainBlock: true` for writes placed immediately after the generator's `Return from injected code` jump (`0x0803924f`) and its `nop`. The API preserves this flag on creation, updates, and public reads; missing or null becomes `false`, including for legacy options. Write order and per-write fields are preserved. No database migration is required. See [examples/main-block-option.json](examples/main-block-option.json).

Legacy submissions with top-level write fields, `primaryWrite`, and `additionalWrites` are normalized into `writes`. Do not mix the two formats. Existing catalog items are normalized on read without changing their IDs, votes, or stored records. Release this API change before the desktop version that submits `writes`; older desktop versions must be updated to consume canonical memory-option responses. No database migration is needed for API records.

The randomizer remains responsible for game addresses, write semantics, and preset validity. Requests and normalized JSON are limited to 256 KiB for presets and 128 KiB for options. Clients must send the option directly, without a local ID, `readOnly`, or a `data` wrapper. See [examples/option.json](examples/option.json).

An option's name is its `comment`, matched without case or surrounding spaces across all categories. A same-name submission updates the stored option only when the authenticated Cognito subject exactly matches its `createdBy`; usernames do not grant permission. A different author or missing stored author returns `403`. Multiple same-name entries owned by the caller return `409` rather than choosing arbitrarily. Updates replace only `data`, preserving the ID, creator, creation time, and votes.

Sharing searches all option pages, including legacy rows and empty pages with cursors. An `option-name` reservation is written transactionally with the item to prevent concurrent duplicate creations. Updates compare the previously read JSON and stored author; concurrent changes return `409`, while concurrent votes are preserved. Name reservations are excluded from catalog lists and are separate from preset names. No table migration is required; deploy the updated Lambda to enable this behavior.

### Presets and generator integration

Submit the generator's **exported preset JSON**, with nonblank `metadata.id` and `metadata.name`. Custom randomizer settings, explicit `false` values, and JSON number precision are preserved. The authenticated subject is recorded independently as `createdBy`. See [examples/preset.json](examples/preset.json).

Sharing a name already in the catalog updates that preset when the caller's verified access-token `username` matches a string in the **stored** `metadata.author` array. Names and author usernames match without case or surrounding spaces. The new payload cannot grant itself update permission. Updates replace only `data`, preserving the catalog ID, creator, creation time, and all votes. A collision without a matching author returns `403`, even if another preset with that name lists the caller as an author; more than one matching preset listing that author returns `409` rather than choosing arbitrarily. Missing or malformed stored author arrays do not authorize an update.

Existing rows work without migration: sharing searches all preset pages, including empty pages with cursors. A separate `preset-name` partition reserves each normalized name transactionally with its write, preventing concurrent submissions from creating duplicate rows. Updates compare the previously authorized JSON before replacing it, so an intervening author change returns `409`. Concurrent votes remain intact. These reservation rows are excluded from public catalog lists. Deploy the updated Lambda before relying on this behavior in the generator; the POST route and table schema are unchanged.

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

### Featured mods

`GET /v1/featured-mods` is public and returns the most recently published featured mod by `createdAt`, as a single flat object. It returns `404 not_found` before any mod is published. The new feature is visible immediately, including when its release time is in the future. `releaseTime` controls only `downloadAvailable`: it is `false` before that instant and `true` at or after it, using the server's clock. A client can show the banner immediately and use this flag to enable its Download button.

```json
{
  "id": "0123456789abcdef0123456789abcdef",
  "title": "A new challenge",
  "description": "Discover a community-made mod for your next castle run.",
  "image": "https://example.com/temporary-signed-image-url",
  "releaseTime": "2027-01-15T18:00:00Z",
  "downloadAvailable": false,
  "downloadUrl": "/v1/featured-mods/0123456789abcdef0123456789abcdef/download",
  "createdBy": "cognito-user-sub",
  "createdAt": "2026-10-08T01:00:00Z"
}
```

`POST /v1/featured-mods` accepts `multipart/form-data` with exactly one of each required field:

| Field | Requirements |
| --- | --- |
| `title` | Nonblank text, 1–200 UTF-8 bytes |
| `description` | Nonblank text, 1–10,000 UTF-8 bytes |
| `releaseTime` | RFC3339 timestamp with an explicit time zone, such as `2027-01-15T18:00:00Z` or `2027-01-15T11:00:00-07:00`; returned in UTC |
| `image` | One valid, decoded PNG, JPEG, or WebP file, at most 2 MiB (2,097,152 bytes) and 4,096 × 4,096 pixels |
| `ppf` | One uploaded `.ppf` file with a recognized PPF1, PPF2, or PPF3 header, at most 4 MiB (4,194,304 bytes) |

PPF checks recognize `PPF10` with method 0 and at least 56 bytes, `PPF20` with method 1 and at least 1,084 bytes, or `PPF30` with method 2 and at least 60 bytes. These are file-header checks; they do not establish whether the patch applies to a particular game image.

The whole multipart body is limited to 4 MiB + 64 KiB (4,259,840 bytes), including both files, text fields, and boundaries. The combined limit can reduce the space available for the PPF when the image is large. Let the HTTP client generate the multipart boundary. JSON bodies, image URL strings, SVGs, and other image formats are not accepted. Unknown or duplicate fields are rejected. A whole-body limit violation returns `413 body_too_large`; invalid fields/files or individual field/file limit violations return `400 invalid_request`. Valid publication returns `201`, `Location: /v1/featured-mods`, and the same flat metadata response format as GET, without PPF bytes. Every successful POST creates a publication; publishing a new one replaces the previous banner selection. Do not automatically retry an uncertain POST; GET first to check what was published.

Production publishing requires a verified Cognito access token whose `cognito:groups` includes `featured-mod-publishers`. The SAM stack creates that group with no members. An administrator must add approved accounts to it before they can publish; ordinary accounts receive `403 forbidden`. After group membership changes, obtain a new access token so it contains the updated groups. Production ignores development headers. For `cmd/local`, send both `X-Dev-User` and `X-Dev-Featured-Publisher: true`.

```sh
curl http://127.0.0.1:8080/v1/featured-mods
curl -X POST http://127.0.0.1:8080/v1/featured-mods \
  -H 'X-Dev-User: alice' -H 'X-Dev-Featured-Publisher: true' \
  --form-string 'title=A new challenge' \
  --form-string 'description=Discover a community-made mod for your next castle run.' \
  --form-string 'releaseTime=2027-01-15T18:00:00Z' \
  -F 'image=@/path/to/headshot.png;type=image/png' \
  -F 'ppf=@/path/to/mod.ppf;type=application/octet-stream'
```

Images and PPF files are stored in a private S3 bucket in AWS. Each metadata response contains a presigned image URL valid for 15 minutes; fetch the metadata again when a URL expires rather than persisting it as a permanent asset address. Local development stores both file types in `.local/featured-mod-images` by default; set `LOCAL_FEATURED_MOD_IMAGES_DIR` to use another directory. The local server returns absolute image URLs such as `http://127.0.0.1:8080/featured-mod-images/{id}.png` (`.jpg` and `.webp` are also supported). Metadata responses use `Cache-Control: no-store`. Only the exact `/v1/featured-mods` path supports GET and POST; other methods return `405` with `Allow: GET, POST`.

The PPF download is separate from metadata. Resolve the relative `downloadUrl` against the API base URL and request it with GET. `GET /v1/featured-mods/{id}/download` is public but checks release time on every request. At or after release it returns `307` with `Location` set to a private S3 download URL valid for 15 minutes; follow the redirect to download the PPF with the attachment filename `{id}.ppf`. In local mode the redirect points to `http://127.0.0.1:8080/featured-mod-files/{id}.ppf`, whose handler also checks the release time so direct URLs cannot bypass it. Before release, the download endpoint returns:

```json
{"error":{"code":"not_released","message":"this mod is not available for download yet"}}
```

The status is `403`; an unknown or malformed featured-mod ID returns `404`. Other methods on the download route return `405` with `Allow: GET`. Unrecognized featured-mod routes return `404`. The metadata always includes `downloadUrl`, even before release, while `downloadAvailable` tells the UI whether to enable Download. Refresh metadata or evaluate the UTC `releaseTime` when that instant arrives; the server remains the authority for release enforcement.

To download a released publication, use its ID from metadata:

```sh
curl -L --output mod.ppf http://127.0.0.1:8080/v1/featured-mods/REPLACE_WITH_FEATURED_MOD_ID/download
```

### Voting

Send `PUT /v1/{options|presets}/{id}/vote` with:

```json
{"value": 1}
```

Use `1` to upvote, `-1` to downvote, or `0` to remove the vote. The response contains the item and current aggregate totals. The voter is always the verified Cognito subject, never a body or header user ID. Votes on nonexistent items return `404` without creating an orphan vote. Transaction conflicts retry internally; a remaining conflict returns `409`, which the client can retry. Totals may include other votes committed before the response was read.

The application returns errors as `{"error":{"code":"invalid_request","message":"..."}}`, with statuses `400`, `401`, `403`, `404`, `405`, `409`, `413`, `415`, or `500`. API Gateway can reject authentication or throttled requests before Lambda runs, using its own response format.

## Local development

Install Go 1.26 or newer and Docker Compose. From this directory:

```sh
docker compose up -d
go run ./cmd/local
```

The local runner creates its table automatically and listens on `http://127.0.0.1:8080`. Docker stores the database in a named volume, so it survives ordinary container restarts. `DYNAMODB_ENDPOINT` optionally selects another local HTTP endpoint. This runner only accepts a loopback database endpoint and uses dummy credentials.

For local writes only, send `X-Dev-User` to choose a test identity. Featured-mod publication also requires `X-Dev-Featured-Publisher: true`. The deployed Lambda has no development-authentication mode.

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

Choose a stack name such as `sotn-preset-api`, a region such as `us-west-2`, and allow SAM to create the function's IAM role. Public read routes intentionally have no authorizer. SAM builds `cmd/api` as the Linux ARM64 `bootstrap` executable with the `provided.al2023` runtime. Stack outputs provide `ApiUrl`, `UserPoolId`, `UserPoolClientId`, `Region`, `TableName`, and `FeaturedModBucketName`.

For a subsequent release, use `sam build` and `sam deploy`. DynamoDB, the user pool, and the featured-mod S3 bucket are retained when the stack is deleted or replaced; retained resources must be managed separately. The table uses on-demand billing, encryption, and point-in-time recovery. API Gateway applies a per-route rate of 20 requests/second and burst of 40. This is a small community catalog design: all options share one partition key and all presets another; high traffic may require a different partition/index layout.

### Accounts and access tokens

The stack creates a username-and-password Cognito user pool and a public app client with **no client secret**, suitable for the desktop app. Register users with a unique username and password using Cognito `SignUp`; no email address, phone number, or confirmation code is required. A pre-sign-up Lambda trigger confirms these accounts automatically. Sign in using `USER_SRP_AUTH` or `USER_PASSWORD_AUTH` through a Cognito SDK. The returned **AccessToken** is sent as `Authorization: Bearer <token>` on submissions and votes. Access tokens last one hour; use the refresh token with `REFRESH_TOKEN_AUTH` to renew them without prompting the user to sign in again. Refresh tokens use Cognito's maximum lifetime of 3,650 days (10 years) and should be kept in OS-protected credential storage.

Cognito doesn't support a literally permanent session. A user must sign in again when the 10-year refresh token expires, when the token is revoked, after an administrator signs the user out globally, or when the account is disabled or deleted. The desktop client must persist the refresh token and renew the access token before or after its one-hour expiration for the long-lived login to work.

API Gateway verifies the issuer, client audience, expiry, and `aws.cognito.signin.user.admin` scope. Featured-mod publication additionally checks the verified `cognito:groups` claim for `featured-mod-publishers`. Lambda additionally requires `token_use=access` and a nonempty `sub` claim. ID tokens and caller-provided identity headers are not accepted. The Cognito methods above issue the required access-token scope. Because accounts have no email address or phone number, Cognito self-service password recovery is disabled; an administrator must reset a forgotten password with `AdminSetUserPassword` or an equivalent administrative workflow.

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
