# Publishing tandem.airforce

`site/build.sh` builds four static pages from `DESIGN.md`, using Pandoc 3.13.
The `Site` GitHub Actions workflow publishes `site/out/` to GitHub Pages when
the draft, site source, or Pages workflow changes on `main`. It can also be
started manually. The repository can remain private; the website is public.

The public pages describe the current design and its open questions. The design's
history is retained in Git rather than published as a separate decision log.

GitHub Pages settings for `jespern/tandem`:

* Build source: GitHub Actions.
* Custom domain: `tandem.airforce` (the canonical hostname).
* Enable **Enforce HTTPS** once GitHub has issued the domain certificate.

Cloudflare DNS for the `tandem.airforce` zone:

| Type | Name | Target | Proxy status | TTL |
|---|---|---|---|---|
| CNAME | `@` | `jespern.github.io` | DNS only | Auto |
| CNAME | `www` | `jespern.github.io` | DNS only | Auto |

Cloudflare flattens the root CNAME into GitHub's IPv4 and IPv6 addresses.
GitHub handles the redirect from `www.tandem.airforce` to `tandem.airforce`.
There is no `/tandem/` path in either DNS target. This Actions-based deployment
does not need a `CNAME` file in the generated site.

Configure the custom domain in GitHub before adding DNS. Domain routing and
certificate issuance can lag behind an otherwise correct DNS change.

Useful checks:

```sh
dig @jamie.ns.cloudflare.com tandem.airforce A +short
dig @jamie.ns.cloudflare.com tandem.airforce AAAA +short
dig @jamie.ns.cloudflare.com www.tandem.airforce CNAME +short
gh api repos/jespern/tandem/pages
gh api repos/jespern/tandem/pages/health
curl -I https://tandem.airforce/
curl -I https://www.tandem.airforce/
```

Primary references:
[GitHub custom domains](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site),
[GitHub HTTPS](https://docs.github.com/en/pages/getting-started-with-github-pages/securing-your-github-pages-site-with-https),
and [Cloudflare root records](https://developers.cloudflare.com/dns/manage-dns-records/how-to/create-zone-apex/).
