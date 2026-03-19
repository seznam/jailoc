# GitLab Pages Custom Domain Setup

This guide configures `zensical.org` as the custom domain for the jailoc documentation site hosted on GitLab Pages.

## Prerequisites

- Access to DNS management for `zensical.org`
- GitLab project maintainer role
- At least one successful Pages pipeline run (so the `public/` artifact exists)

## Step 1: Add the Custom Domain in GitLab

1. Navigate to the project: **Settings → Pages**
2. Click **New Domain**
3. Enter `zensical.org` as the domain
4. GitLab will show you a **verification TXT record** — copy it

## Step 2: Configure DNS

Add the following DNS records at your DNS provider for `zensical.org`:

| Type | Name | Value |
|------|------|-------|
| A | `@` | GitLab Pages IP (see [GitLab docs](https://docs.gitlab.com/ee/user/project/pages/custom_domains_ssl_tls_certification/)) |
| CNAME | `www` | `zensical.org` |
| TXT | `_gitlab-pages-verification-code.zensical.org` | `gitlab-pages-verification-code=<code from Step 1>` |

> **Note:** GitLab.com Pages IP is `35.185.44.232`. For self-hosted GitLab instances, check with your infrastructure team.

## Step 3: Verify the Domain

1. Return to **Settings → Pages** in GitLab
2. Click the **Verify** button next to `zensical.org`
3. Wait for DNS propagation (up to 24–48 hours)
4. GitLab will show a green checkmark once verified

## Step 4: Enable HTTPS

GitLab Pages automatically provisions a Let's Encrypt certificate after domain verification.

1. In **Settings → Pages**, check that HTTPS is enabled for `zensical.org`
2. Force HTTPS redirect is recommended — enable it in the same settings panel

## Verification

Once configured, the site will be available at:

- `https://zensical.org` — main documentation
- `https://zensical.org/downloads/` — binary downloads directory

## Troubleshooting

**Domain not verified after 48 hours:**
- Confirm the TXT record is live: `dig TXT _gitlab-pages-verification-code.zensical.org`
- Check that the A record resolves: `dig A zensical.org`

**Pages not updating after pipeline:**
- Confirm the `pages` job completed successfully in the pipeline
- Check that `pages.publish: public` is set in `.gitlab-ci.tbc.yml`
