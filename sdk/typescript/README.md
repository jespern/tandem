# TypeScript SDK

Tandem v2 core for **Node.js 26+**, with a browser build for Liszto.
See the [shared scope and real-time contract](../README.md).
The [browser guide](BROWSER.md) covers JMAP, IndexedDB, browser keys, worker
capsules, the durable device controller and files (DESIGN §17). The pure file
core, chunk messages and an in-memory blob folder are in the main entry point.

```sh
cd sdk/typescript
npm ci --ignore-scripts
npm test
```

Builds produce ESM JavaScript and declarations in `dist/`, plus browser and worker
bundles. Node keeps native crypto; the browser adds Noble crypto and a Buffer
implementation. Install the built package locally before publication; it is
marked private while the complete v2 device protocol is unfinished.

Given a `PushStore`, receive and send through one session:

```typescript
import { Session } from '@tandem/protocol';

const session = new Session(store, { cursor: savedCursor, capacity: 64 });
await session.start();
try {
  for await (const batch of session.updates()) {
    await applyValidatedBatchAndSaveCursor(batch);
  }
} finally {
  await session.close();
}
```

Another task can `await session.submit(tag, persistedEnvelope, abortSignal)`.
Abort signals cancel waiting; they cannot promise a command already sent was not
received. Updates apply backpressure when the consumer is slow. Browser transport
is available through JMAP. Node can also import `JMAPStore` and `fastmailConfig`
from the main entry point; see [JMAP setup](../JMAP.md). The browser controller
adds verified reads/edits, pairing, sharing, key rotation and healing.

Node's native crypto supplies signing, hashing, X25519 and AEAD. Strict Ed25519
verification uses a public-input equation check because the native verifier rejects
some signatures that the protocol accepts. It never processes secret scalars.
Epoch counters in merge entries use `bigint`; wire HLC millis/counters use numbers
within their specified 48/16-bit bounds. Protocol JSON rejects lossy numeric tokens
before conversion to JavaScript numbers.
