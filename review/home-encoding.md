# Home field encodings: proposed schema

Decided 2026-11-12 (Jesper, on Sol's review, finding C): every protocol-defined home field gets a specified byte encoding, agreed between the SDKs. This is what the Go engine (`sdk/go/device`) writes today, offered as the schema. `conformance/journal.py` on `main` differs in the `flight` note (it uses `id`/`key` and writes JSON `null` to clear it), so one of the two has to move; the Go engine already reads `null` as "no change".

All home records are named records (§7.3). `<collectionID>` and `<inviteID>` in names are b64u. Values are the bytes of the `v` field of an `assign`, or the `e` of an `add`.

## `membership:<collectionID>`

| Field | Op | Value bytes |
|---|---|---|
| `_name` | assign | The record name, UTF-8: `membership:<b64u collection ID>` |
| `owner` | assign | The owner's user ID: 32 raw bytes |
| `role` | assign | ASCII `owner`, `writer` or `reader` (cached from the roster) |
| `state` | assign | ASCII `requested`, `active`, `left` or `removed` |
| `keys` | add (set) | One element per epoch key: UTF-8 JSON `{"epoch":"<b64u 32-byte epoch-start ID>","k":"<b64u 32-byte key>"}`, members in that order, no whitespace |
| `flight` | assign | A pending key change: UTF-8 JSON `{"parent","stmt","k","members","what","device"}`, where `parent` and `stmt` are b64u 32-byte statement IDs, `k` the b64u 32-byte key, `members` the members the change leads to in the roster's member form (§8.1), `what` a short description for the user, `device` the name of the device that started it. **No pending change:** JSON `null` (preferred) or an empty value. A value that isn't one of these, or names no statement, is treated as no change. |

## Open

- Whether `keys` elements may be written by other serializers with other member order or whitespace. The element bytes are the set element, so two spellings are two elements: either the schema fixes the bytes, as above, or readers deduplicate by epoch ID. The Go reader already reads every element and indexes by epoch ID.
- `invite:<inviteID>` and `settings:<app>` aren't written by the engine yet; their fields come with the invite flow.
