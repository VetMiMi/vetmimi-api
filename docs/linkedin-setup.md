# LinkedIn app setup

One-time steps so the publishing portal can post to Daw Mi's own LinkedIn
profile. Until they are done, the LinkedIn channel fails `not_connected` and
Daw Mi posts it by hand (copy & open). Both products below are self-serve,
so the app needs no review.

## Before you start

- A LinkedIn **Company Page** for VetMiMi exists, with Daw Mi as a Page
  admin: LinkedIn requires every app to be associated with one. The app posts
  to her personal profile, not to the Page.
- The site's Privacy page is live at `{SITE_URL}/privacy`.

## Create the app

1. Sign in to [linkedin.com/developers](https://www.linkedin.com/developers/apps)
   as Daw Mi and choose **Create app**. Name it `VetMiMi Publishing`, pick the
   VetMiMi Company Page, set the privacy policy URL to `{SITE_URL}/privacy`,
   upload a logo, and accept the terms. Verify the app from the Page when
   LinkedIn asks.
2. **Products**: request **Sign In with LinkedIn using OpenID Connect** and
   **Share on LinkedIn**. Both are granted at once. Together they give the
   scopes the API asks for: `openid profile w_member_social`.
3. **Auth** → OAuth 2.0 settings → Authorized redirect URLs for your app: add
   `{SITE_URL}/admin/settings/connections/linkedin`, e.g.
   `https://vetmimi-next.vercel.app/admin/settings/connections/linkedin`
   (exactly; the API sends this address, built from `SITE_URL`).
4. **Auth** → copy the **Client ID** and the **Primary Client Secret**.

## Configure the API

On the live host, in `.env`:

```sh
LINKEDIN_CLIENT_ID=<Client ID>
LINKEDIN_CLIENT_SECRET=<Primary Client Secret>
# Optional; the LinkedIn-Version header (YYYYMM) the API sends.
LINKEDIN_API_VERSION=202609
```

`MEDIA_PUBLIC_URL` must be reachable from the live host: the worker reads
each image from it and uploads it to LinkedIn. Restart the api and worker
containers.

## Connect

Daw Mi signs in to the admin, opens **Settings → Connections**, chooses
**Connect LinkedIn**, and allows the app to post for her. The Connections
page shows her name and when access ends. LinkedIn tokens last about 60
days and this app cannot renew them by itself, so the status turns
`expiring_soon` a week before and `reconnect_required` once they lapse (or
if LinkedIn refuses the token): she connects again to renew.

LinkedIn supports each `LINKEDIN_API_VERSION` for at least a year; raise it
to a newer `YYYYMM` before then.
