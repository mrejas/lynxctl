# Notes on the IoT Open API documentation

Observations made while building iotopenctl against
<https://lynx.iotopen.se/api-doc/> (REST), <https://lynx.iotopen.se/api-doc/mqtt/>
and <https://lynx.iotopen.se/tech/docs/platform/>. Each note says whether it
was **verified** against the server or is **from the docs only**.

## Errors

1. **`/api/v2/perimission` does not exist.** The Permissions section
   documents `GET /api/v2/perimission`. The server answers 404 on that path
   and 401 (i.e. it exists) on `/api/v2/permission`. *Verified* with
   unauthenticated requests.
2. **Extra slash in a path.** `GET /api/v2/installationinfo/{client_id}` is
   shown with a double slash (`installationinfo//{client_id}`). *Docs only.*
3. **"List" endpoint with an ID.** User registration: the endpoint described
   as listing registration policies has the path
   `GET /api/v2/user/registration/policy/{policy_id}`. Either the
   description or the path is wrong. *Docs only.*
4. **`POST /api/v2/user` body typed as an array.** The body is typed `[User]`,
   but the example and the response are a single user. *Docs only.*
5. **Typo:** edge app version upload, 204 response: "The provided files
   *where* identical" → "were". *Docs only.*
6. **Client library name.** The API index page calls the React library
   "react-node" in the text; the package is `react-lynx`. *Docs only.*

## Missing or contradictory information

### Authentication

7. **2FA field name.** The login response documents `nextstep` with the
   value `"smslogin"`. Older scripts look for `next_step` containing
   `"challenge"`. Which is right (or both, depending on version)?
   iotopenctl accepts both. *Docs only.*
8. **Login request body is undocumented.** Clients send
   `{"device_id": ..., "app_version": ...}` with `POST /api/v2/auth`; the docs
   list no body at all. What are these fields used for? *Docs only.*

### Topics and values

9. **Relative or full topics are never stated.** Status and log responses
   contain topics prefixed with the client ID (`100/obj/knx/0/2/4`), while
   the MQTT publish example uses a relative topic (`obj/device/1/temperature`).
   Whether `topic_read`/`topic_write` meta values, the `topics` filters and
   schedule topics are relative or full is not documented anywhere; it can
   only be inferred from examples. A short "Topics" section would help.
10. **`topics` filter syntax differs.** The v3 log accepts repeated
    parameters *or* a comma-separated list; status documents only repeated
    parameters. *Docs only.*
11. **Is the v3 log always aggregated?** `aggr_method` is documented with
    the default `median` and `aggr_interval` with an automatic default
    (1m–6h depending on the range). That reads as if every response is
    aggregated even when the client does not ask for it. If raw values are
    returned without these parameters, the defaults should say "only when
    the other aggregation parameter is set". The POST variant does not list
    the aggregation parameters at all. *Docs only.*
12. **MQTT payload values are numbers only.** The value message requires
    `value` to be a number. Worth stating explicitly, since nothing says
    how text or JSON values are sent. The WebSocket endpoint lists no port.
    *Docs only.*

### Devices and functions

13. **PUT bodies differ.** `PUT /api/v2/devicex/...` takes the full `ModelX`
    (with `id`, timestamps), `PUT /api/v2/functionx/...` takes `ModelXNoID`.
    Is the difference real? *Docs only.*
14. **Meta values typed as strings, example with a number.** The DeviceX
    example has `"device_id": 200`, while meta is documented as a map of
    strings. iotopenctl accepts both. *Docs only.*
15. **Filtering functions by device.** The common
    `functionx?device_id=<id>` filter works through the generic meta
    filter, but it is not mentioned. An example would help. *Docs only.*
16. **Known meta keys.** The "Known meta-keys" page lacks `topic_write`,
    `device_id` and the keys used by each function type (it refers to a
    separate page). *Docs only.*

### Installations

17. **`protected_meta` asymmetry.** The installation request schema has
    `protected_meta`, the response schema does not. *Docs only.*

### Search

18. **Different conventions from the rest of the API.** The search response
    uses camelCase (`clientID`, `installationID`, `organizationID`,
    `metaMatches`) where everything else uses snake_case, returns `clientID`
    as a string where it is a number elsewhere, filters metadata with
    `metadata.key=value` where lists use `key=value`, and returns errors as
    `{"error": ...}` instead of `{"message": ...}`. *Docs only.*

### Schedules

19. **The `Schedule` schema is missing** from the Schemas section; the
    fields can only be read from examples. *Docs only.*
20. **Timestamp types differ:** `created_at` is an integer, `updated_at` a
    string (described as a Unix timestamp). *Docs only.*
21. **0-indexed months?** `month` is described as 0-indexed, unlike cron
    (1–12), and `day_of_week` as 0-indexed with Sunday as both 0 and 7.
    Please confirm, and say whether ranges (`1-5`) and steps (`*/15`) are
    supported or only comma lists. *Docs only.*
22. **`POST /api/v2/schedule/{installation_id}` documents no response
    body**, unlike other create endpoints. *Docs only.*

### Organizations, users and tokens

23. **Organization examples are inconsistent:** `children[].name` is `0` (a
    number); `PUT /api/v2/organization/{id}` lists no body; the PUT response
    example has a numeric `zip` and lacks `protected_meta` and
    `password_valid_days`; the `minimal=true` response is not shown.
    *Docs only.*
24. **Token value in lists.** The `token` field is documented as "only
    available when creating a new token", but the list example includes a
    token value. The `CreateToken` schema is missing from the Schemas
    section. *Docs only.*
25. **Password change body** is typed as a plain object; the fields
    (`current_password`, `new_password`) only appear in the example, and it
    is not stated whether `current_password` is required when changing
    another user's password. *Docs only.*

### Edge apps, notifications and files

26. **Edge app updates go to the collection.** `PUT /api/v2/edge/app`
    (with the ID in the body) differs from all other resources, which use
    `PUT .../{id}`. There is no endpoint for deleting an app. *Docs only.*
27. **`configure` response.** `GET /api/v2/edge/app/{app_id}/configure`
    documents the response as "None" although it returns app.json.
    *Docs only.*
28. **`version` parameter of download/configure.** Is it the version name
    (`1.2.0`) or the hash? iotopenctl sends the name. *Docs only.*
29. **Notification send body.** `POST .../output/{output_id}/send` has no
    body schema; only the description says a JSON object becomes available
    to templates. *Docs only.*
30. **File upload field.** `POST /api/v2/file/...` types the `filename`
    multipart field as an array of strings, while `PUT` types it as binary.
    The download endpoint says the format can be chosen with `Accept` but
    not which formats exist. *Docs only.*
31. **Delete messages vary in case:** `"Deleted"` for most resources,
    `"deleted"` for schedules and edge app instances. *Docs only.*

## Suggestions

- An OpenAPI/Swagger file linked from the API page would make the docs
  machine-checkable and would have caught several of the issues above
  (missing schemas, the misspelled path).
- A short "Conventions" section: topic form (relative vs. with client ID),
  meta filters, error format, timestamps (seconds, with fractions).
