# Meta app setup

One-time steps so the publishing portal can post to Daw Mi's own Facebook Page
and the Instagram account linked to it. Until they are done, Facebook and
Instagram channels fail `not_connected` and Daw Mi posts them by hand (copy &
open). Only Daw Mi's own Page is used, so the app stays with Standard Access
and needs no App Review.

## Before you start

- Daw Mi's Instagram is a **Business** (or Creator) account, linked to her
  Facebook Page (Page settings → Linked accounts → Instagram).
- Daw Mi has full control of the Page, and of the business portfolio if a
  business portfolio owns it.
- The site's Privacy page is live at `{SITE_URL}/privacy`, e.g.
  `https://vetmimi-next.vercel.app/privacy`.

## Create the app

1. Sign in to [developers.facebook.com](https://developers.facebook.com) as
   Daw Mi (or add her as an app **Administrator** under App roles afterwards;
   she must be one, since only app roles may use an app in Standard Access).
2. Create app → use case **Manage everything on your Page** (Facebook Login for
   Business) → type **Business**. Name it `VetMiMi Publishing`.
3. Add the product **Facebook Login for Business**. Under its Settings:
   - Valid OAuth Redirect URIs: `{SITE_URL}/admin/settings/connections`, e.g.
     `https://vetmimi-next.vercel.app/admin/settings/connections` (exactly; the
     API sends this address, built from `SITE_URL`).
   - Leave Client OAuth login and Web OAuth login on; turn on Enforce HTTPS.
4. Facebook Login for Business → Configurations → Create configuration:
   token type **User access token**, and these permissions, all at Standard
   Access: `pages_show_list`, `pages_manage_posts`, `pages_read_engagement`,
   `instagram_basic`, `instagram_content_publish`, and `business_management`
   (needed when a business portfolio owns the Page). Copy its
   **Configuration ID**.
5. App settings → Basic: set the Privacy Policy URL to `{SITE_URL}/privacy`,
   an app icon and a category, and copy the **App ID** and **App Secret**.
   Under App settings → Advanced, turn on **Require App Secret** (the API
   signs every call with `appsecret_proof`).
6. Switch the app from Development to **Live** (the toggle at the top).

## Configure the API

On the live host, in `.env`:

```sh
META_APP_ID=<App ID>
META_APP_SECRET=<App Secret>
META_CONFIG_ID=<Configuration ID>
# Optional; the Graph API version the API calls.
META_GRAPH_VERSION=v26.0
```

`MEDIA_PUBLIC_URL` must be reachable from the internet: Facebook and Instagram
fetch each image from it. Restart the api and worker containers.

## Connect

Daw Mi signs in to the admin, opens **Settings → Connections**, chooses
**Connect Facebook**, and approves the permissions for her Page and Instagram
account. With one Page it is connected at once; otherwise she picks it.
The Connections page shows the Page, the Instagram account and when access
ends: Meta stops a token's data access after 90 days without her signing in
again, so she connects again before that date, or whenever it shows
`reconnect_required`.

Meta retires a Graph API version about two years after its release; raise
`META_GRAPH_VERSION` before then.
