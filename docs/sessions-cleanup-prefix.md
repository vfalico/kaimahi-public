# Sessions cleanup ownership prefix

The companion Sessions change journals one host-authored `COMPUTE_OWNED` lifecycle record before exact suspended-compute Resume. It is cleanup-only metadata, not an execution start, activation proof or permission for Kaimahi to release compute. This change accepts that bounded known record before the existing invocation, preserving the full chain, exact requirement bytes and original sequence/hash for reference replay.

The live stream requires first-record position, no execution ID, known schema/fields, matching created Session UID, selected harness and exact submitted opaque requirement. It then requires the turn's ComputeInstance to match the ownership record. Duplicates, policy-free ownership prefixes, other lifecycle kinds, unknown wire fields, malformed IDs, changed policy/UID, or snapshots refuse. Frame/byte bounds still count the prefix. Ordinary policy-free sequences remain unchanged. Tool evidence collection begins only at the invocation; cleanup metadata cannot manufacture tool calls or model results.

Offline verification retains the entire original chain and replays it through the pinned controller/reference harness without live effects. The invocation-shape view may skip exactly one validated cleanup-only record; it does not renumber or rehash records. A prefix naming a different receipt Session refuses. Kaimahi remains a policy carrier and evidence consumer: it gains no Guardd transport, target lookup, activation command or lifecycle control.

## Qualification

Required companion: exact Sessions generated API containing `Lifecycle.COMPUTE_OWNED` and ownership fields. Workspace setup uses exact unpublished companion revisions; no published SDK alone establishes compatibility. Tests cover actual hash and controller reference replay with explicit fake remote streams. Native public integration remains a separate opt-in qualification, not normally deployed operator acceptance. This patch does not close uncertain Create responses, arbitrary crash cuts or runtime-native cleanup obligations.
