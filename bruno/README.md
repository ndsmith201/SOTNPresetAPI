# Bruno collections

Use **Open Collection** in Bruno and select one of these directories:

| Directory | Contents |
| --- | --- |
| `api` | All twelve API operations, separate upvote/downvote/remove-vote examples, and authentication helpers |
| `auth` | Standalone Cognito Get token and Refresh token requests |

## AWS setup

1. Open `api` and select the **AWS** environment.
2. Open the environment editor and enter the test account password in the **password** secret variable. The username is already `test-user`; change it to use another account.
3. Send **Authentication → Get token**. It stores `access_token` and `refresh_token` as runtime variables for this collection.
4. Send API requests. Submissions, votes, and featured-mod publication automatically attach the access token. Public GET requests need no token. Publishing requires membership in the `featured-mod-publishers` group in addition to signing in; the stack creates this group empty, so an administrator must add the publishing account.
5. Use **Authentication → Refresh token** when the one-hour access token expires. The deployed app client gives refresh tokens Cognito's maximum 3,650-day lifetime; sign in again if the refresh token expires or is revoked.

The environment points to the `sotn-api` deployment in `us-east-1`. For another deployment, update `base_url`, `cognito_url`, and `cognito_client_id` using the stack outputs. No AWS access keys or client secret are required.

Alternatively, copy `.env.example` to `.env` inside the collection directory and set `SOTN_TEST_PASSWORD` there. `.env` is ignored by Git. The environment's password secret takes precedence. Password values and issued tokens are not included in the committed collection files; scripts retain tokens only in runtime variables. Request/response views still contain credentials and tokens, so exclude those values from shared exports and reports.

The standalone `auth` collection uses the same setup and login flow. Runtime variables are scoped to their collection. To use its token in `api`, copy `AuthenticationResult.AccessToken` from the response into the API environment's `access_token` secret, or run the API collection's own Get token request. Use the **access token**, not the ID token. These requests expect an account that can sign in directly; username-only self-sign-ups are confirmed automatically. They do not implement MFA or password-change challenges.

## Using the API requests

- **Create option** and **Create preset** contain editable example bodies. Each successful send creates a new submission and saves its catalog ID as `option_id` or `preset_id` for subsequent Get and Vote requests.
- To target an existing submission, set the corresponding ID in the environment. If you already created an item during this session, clear or update its runtime ID first because runtime variables take precedence.
- Vote requests set the current account's vote to `1`, `-1`, or `0`. Zero removes the vote.
- **Get current featured mod** returns a flat object (or `404` before the first publication). A future `releaseTime` does not hide the banner; it sets `downloadAvailable: false`. AWS image URLs expire after 15 minutes, so fetch metadata again for a fresh URL.
- **Publish featured mod** is an explicit multipart upload. Set `featured_image_path` to a local PNG, JPEG, or WebP file and `featured_ppf_path` to a `.ppf` file in the selected environment, then edit `title`, `description`, and `releaseTime` in **Body**. `releaseTime` needs an explicit time zone. Images must be at most 2 MiB and 4,096 × 4,096 pixels. PPF files must have a recognized PPF1/PPF2/PPF3 version/method header and be at most 4 MiB; the total multipart body, including both files, is limited to 4 MiB + 64 KiB. Bruno sets the multipart boundary automatically. A successful send returns `201` and makes this entry the current featured banner immediately. The PPF is retrieved separately through the returned relative `downloadUrl`; metadata includes no PPF bytes. Get and Publish save `featured_mod_id` for the download request.
- **Download PPF** uses `featured_mod_id` and disables redirect following to expose the API response. It returns `403 not_released` before `releaseTime`, or `307` with a PPF file URL at or after release. Enable **Follow redirects** in **Settings** to retrieve the bytes. AWS file URLs expire after 15 minutes and use attachment filename `{id}.ppf`. Local direct-file URLs also enforce release time.
- List requests fetch one page and save `nextCursor`. Enable the disabled `cursor` parameter in **Params** to fetch the next page. Stop when the response has no `nextCursor`; disable the cursor parameter to start over. `limit` defaults to 20 and supports 1–50.

Running the full API collection against AWS creates real catalog submissions, changes votes, and publishes a featured mod if both files are configured and the account is authorized. The API has no delete endpoint for submissions or featured publications. Send **Publish featured mod** explicitly after checking its fields; avoid recursive whole-collection runs for routine testing.

## Local testing

From the repository root, start DynamoDB Local and the API:

```sh
docker compose up -d
go run ./cmd/local
```

Open the `api` collection and select **Local**. Writes use `X-Dev-User: alice`; change `dev_user` to test another voter. **Publish featured mod** also sends `X-Dev-Featured-Publisher: true` for local testing. Production ignores these headers and verifies the token instead. Authentication requests automatically skip in this environment.

If the Bruno CLI is installed, run the ordinary catalog checks from `bruno/api` without invoking featured-mod publication:

```sh
bru run 01-health.bru --env Local --bail
bru run 02-options -r --env Local --bail
bru run 03-presets -r --env Local --bail
bru run 04-featured-mods/01-get.bru --env Local --bail
```

After setting `featured_image_path` and `featured_ppf_path` and reviewing the multipart fields, explicitly publish and read it back:

```sh
bru run 04-featured-mods/02-publish.bru --env Local --bail
bru run 04-featured-mods/01-get.bru --env Local --bail
bru run 04-featured-mods/03-download.bru --env Local --bail
```

For AWS publishing, use the AWS environment, obtain an access token issued after the account was added to `featured-mod-publishers`, and run only the reviewed publish request. Ordinary signed-in accounts receive `403`.

To run only login and refresh from `bruno/auth`, supply the password via that collection's `.env` or the `SOTN_TEST_PASSWORD` process environment variable, then run:

```sh
bru run -r --env AWS --bail
```

Successful requests include status tests; authentication also checks for an access token, and vote requests check consistency of the returned totals.

Bruno upload references: [Multipart Form body](https://docs.usebruno.com/send-requests/REST/body-data) and [official BRU multipart syntax example](https://github.com/usebruno/bruno/blob/main/tests/response-examples/fixtures/collection/multipart-example.bru).
