# Bruno collections

Use **Open Collection** in Bruno and select one of these directories:

| Directory | Contents |
| --- | --- |
| `api` | All nine API operations, separate upvote/downvote/remove-vote examples, and authentication helpers |
| `auth` | Standalone Cognito Get token and Refresh token requests |

## AWS setup

1. Open `api` and select the **AWS** environment.
2. Open the environment editor and enter the test account password in the **password** secret variable. The username is already `test@example.com`; change it to use another confirmed account.
3. Send **Authentication → Get token**. It stores `access_token` and `refresh_token` as runtime variables for this collection.
4. Send API requests. Submissions and votes automatically attach the access token. Public GET requests need no token.
5. Use **Authentication → Refresh token** when the access token expires. Sign in again if the refresh token expires or is revoked.

The environment points to the `sotn-api` deployment in `us-east-1`. For another deployment, update `base_url`, `cognito_url`, and `cognito_client_id` using the stack outputs. No AWS access keys or client secret are required.

Alternatively, copy `.env.example` to `.env` inside the collection directory and set `SOTN_TEST_PASSWORD` there. `.env` is ignored by Git. The environment's password secret takes precedence. Password values and issued tokens are not included in the committed collection files; scripts retain tokens only in runtime variables. Request/response views still contain credentials and tokens, so exclude those values from shared exports and reports.

The standalone `auth` collection uses the same setup and login flow. Runtime variables are scoped to their collection. To use its token in `api`, copy `AuthenticationResult.AccessToken` from the response into the API environment's `access_token` secret, or run the API collection's own Get token request. Use the **access token**, not the ID token. These requests expect a confirmed account that can sign in directly; they do not implement MFA or password-change challenges.

## Using the API requests

- **Create option** and **Create preset** contain editable example bodies. Each successful send creates a new submission and saves its catalog ID as `option_id` or `preset_id` for subsequent Get and Vote requests.
- To target an existing submission, set the corresponding ID in the environment. If you already created an item during this session, clear or update its runtime ID first because runtime variables take precedence.
- Vote requests set the current account's vote to `1`, `-1`, or `0`. Zero removes the vote.
- List requests fetch one page and save `nextCursor`. Enable the disabled `cursor` parameter in **Params** to fetch the next page. Stop when the response has no `nextCursor`; disable the cursor parameter to start over. `limit` defaults to 20 and supports 1–50.

Running the full API collection against AWS creates real catalog submissions and changes votes. The API has no delete endpoint for submissions. Use the Local environment for a full test run.

## Local testing

From the repository root, start DynamoDB Local and the API:

```sh
docker compose up -d
go run ./cmd/local
```

Open the `api` collection and select **Local**. Writes use `X-Dev-User: alice`; change `dev_user` to test another voter. Authentication requests automatically skip in this environment.

If the Bruno CLI is installed, run from `bruno/api`:

```sh
bru run -r --env Local --bail
```

To run only login and refresh from `bruno/auth`, supply the password via that collection's `.env` or the `SOTN_TEST_PASSWORD` process environment variable, then run:

```sh
bru run -r --env AWS --bail
```

Successful requests include status tests; authentication also checks for an access token, and vote requests check consistency of the returned totals.
