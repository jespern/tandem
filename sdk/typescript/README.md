# TypeScript SDK

Native Tandem v2 core for **Node.js 26+**. Browser transport is outside this milestone.
See the [shared scope and real-time contract](../README.md).

```sh
cd sdk/typescript
npm ci --ignore-scripts
npm test
```

Builds produce ESM JavaScript and declarations in `dist/`. There are no runtime
package dependencies. Install the built package locally before publication; it is
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
received. Updates apply backpressure when the consumer is slow. The IMAP adapter
and complete device engine remain to do.

Node's native crypto supplies signing, hashing, X25519 and AEAD. Strict Ed25519
verification uses a public-input equation check because the native verifier rejects
some signatures that the protocol accepts. It never processes secret scalars.
Epoch counters in merge entries use `bigint`; wire HLC millis/counters use numbers
within their specified 48/16-bit bounds. Protocol JSON rejects lossy numeric tokens
before conversion to JavaScript numbers.
