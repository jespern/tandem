# Fastmail browser setup and live probe

Fastmail’s [API documentation](https://www.fastmail.com/dev/) specifies the JMAP
Session URL and Bearer API tokens, with browser CORS support. API tokens are for
personal use/testing; a distributed web app needs registered OAuth redirect URIs
and allowed origins. An IMAP app password is a separate credential.

```ts
import {BrowserDevice, BrowserJournal, BrowserKeychain, JMAPStore,
        fastmailConfig} from '@tandem/protocol/browser';

const journal = await BrowserJournal.open('liszto-sync');
const keychain = await BrowserKeychain.open('liszto-keys');
const config = fastmailConfig(apiToken);
await keychain.save('mail-connection', new TextEncoder().encode(JSON.stringify(config)));
const device = await BrowserDevice.pair(pairingLink, 'Browser', journal, keychain);
const running = device.run(() => JMAPStore.open(config), signal);
for await (const update of device.updates(signal)) {
  // Render device.state only after its authority checks; update.status reports readiness.
}
await running;
await device.close(); journal.close(); keychain.close();
```

After reload, load/decode the wrapped connection bytes with `keychain.get`, then
call `BrowserDevice.resume(hub, journal, keychain)`. Never infer a JMAP token from
the IMAP credentials in a pairing link. Same-origin code can use remembered
credentials; the keychain is browser-profile storage, not hardware isolation.

The Fastmail preset explicitly trusts HTTPS `api.fastmail.com` and its regional
API subdomains, such as `ams.api.fastmail.com`, on the standard HTTPS port.
Other JMAP configs still require exact `trustedOrigins` for additional hosts.
`folderName` defaults to `Tandem`; set it only when intentionally selecting a
separate replica mailbox. Cursors bind to the account and mailbox ID.

## Reproduce the provider check

Build first (`npm ci --ignore-scripts && npm run build`), install Playwright
Chromium (`npx playwright install chromium`), and run from `sdk/typescript`:

```sh
node probe-fastmail.mjs --token-file /path/to/jmap-api-token \
  --imap-password-file /path/to/imap-app-password
```

Credential files contain bare values, including when their filename ends in
`.json`. Without the IMAP argument, only the direct browser checks run. Optional
`--imap-user` overrides the session username; `--timeout-ms` sets the safety
deadline (default 120000). The Python helper needs Python 3.14+ for IMAP IDLE.
No credentials go in argv, reports, screenshots or traces. TLS verification stays
on. Chromium makes real cross-origin requests from a synthetic HTTPS app origin;
there is no API proxy. Only test HTML and the SDK bundle are intercepted.

The probe creates a unique `Tandem-probe-…` mailbox, uses one persistent SSE
subscription, checks exact bytes, replay and push removal, and deletes its own
messages and mailbox on exit. Its IMAP helper scans only after an IDLE event.
Neither transport polls. Normal user mail and the `Tandem` mailbox are untouched.

## Observed result, 14 November 2026

One run on the Amsterdam API region:

| Path/check | Result |
| --- | --- |
| Browser CORS + authenticated SSE | Pass |
| Browser append → browser push/read | 265 ms |
| JMAP append → IMAP IDLE/read, exact bytes | 60,633 ms |
| IMAP append → browser push/read, exact bytes | 587 ms |
| Cursor replay, push removal, cleanup | Pass |

These are single-run end-to-end measurements, not latency guarantees. The large
JMAP→IMAP delay prevents immediate native updates when native clients use IMAP
and browsers use JMAP on this provider. Keeping a connection open does not remove
that delay. No polling workaround is implemented. Test the chosen transport mix
before shipping Liszto; separate profiles may also need separate API tokens for
concurrent SSE subscriptions. This run did not test token concurrency limits.
