# Backing up a production Vector's data (before/instead of clear-user-data)

Investigated 2026-07-14. Question: what can be preserved from a locked production
Vector's /data partition when migrating to wire-pod, for an owner who does not want
to wipe the bot's memories.

## Summary table

| Data type | Backup possible? | Method |
|---|---|---|
| Face recognition data (biometric templates) | **No — impossible via any API** | Only name+ID+timestamps are ever exposed (`LoadedKnownFace` protobuf) |
| Enrolled face names/IDs (metadata) | Yes | wire-pod SDK app `/api-sdk/get_faces`, or `anki_vector.faces` |
| Photos Vector has taken | **Yes** | wire-pod SDK app: `/api-sdk/get_image_ids` then loop `/api-sdk/get_image?id=N&serial=<ESN>` (raw JPEGs); thumbnails via `get_image_thumb`. Or official Python SDK `anki_vector.photos` |
| Settings (`vic.RobotSettings`) | Partially | Bot pushes/pulls jdocs to wire-pod's `jdocs.json` when connected without wiping; force-refresh via `/api-sdk/get_sdk_settings`; then manually copy `jdocs.json` |
| Lifetime stats (`vic.RobotLifetimeStats`) | Partially (on-demand only) | `/api-sdk/get_robot_stats` pulls it into `jdocs.json`; manual copy |
| Full /data partition (incl. faces) | **No** | Requires OSKR unlock — a per-robot image signed by DDL, whose signing service is defunct (DDL assets under court-appointed receiver as of 2026-07-09, case GD-25-013191, Pittsburgh). No exploit exists; CPU fuses + signed-boot chain per kercre123/unlocking-vector |

## Key facts

- **Faces**: the only face API is `RequestEnrolledNames` → `LoadedKnownFace` (verified
  in this repo's vendored protobuf, `vector-cloud/internal/proto/external_interface/
  messages.pb.go:3529`): FaceId, Name, timestamps. No embedding field exists in any
  SDK (Python, .NET, Go) or community tool (Vector Explorer, Cyb3rVector). Anki's
  "face data never leaves the robot" is enforced at the protocol level. It can be
  listed or wiped — never exported.
- **Photos**: wire-pod's built-in SDK app already implements full photo download
  (`chipper/pkg/wirepod/sdkapp/server.go`) over the normal authenticated SDK channel.
  No unlock needed. Works once the bot is associated with wire-pod.
- **Settings/stats**: wire-pod persists whatever jdocs the bot pushes into its local
  `jdocs.json` (`vars.AddJdoc`); `jdocspinger.go` re-pulls RobotSettings
  automatically; lifetime stats only pulled when the SDK app asks. There is NO
  export/backup button in wire-pod (grepped: none) — copy `chipper/jdocs/jdocs.json`
  manually after force-pulling.
- **The escape-pod firmware flash does not grant filesystem access** — it's a
  production-signed OTA that only repoints cloud endpoints. Not a backdoor to /data.
- **OSKR unlock is gone**: requires DDL to sign a per-robot unlock image; DDL lost
  its GitHub org (2024), cloud endpoints dead since ~2023, and as of 2026-07-09 its
  digital assets are under a court-appointed receiver. Whether the signing keys even
  still exist is unknown.

## Resulting strategy for a no-wipe migration

1. Onboard the bot to wire-pod WITHOUT clearing user data (officially supported
   since Nov 2022). The flash already preserved /data; faces/photos are intact.
2. Immediately back up everything backupable:
   - loop the photo endpoints → save all JPEGs
   - hit `/api-sdk/get_sdk_settings` and `/api-sdk/get_robot_stats` → then copy
     `chipper/jdocs/jdocs.json` (and `botConfig.json`/`botSdkInfo.json`) somewhere safe
3. Live with the bot; if "weird behavior" (auth flakiness, settings reverts) proves
   intolerable, clear user data THEN — at that point only the face enrollments are
   truly lost (re-enrollable in minutes), since photos/settings/stats were saved in
   step 2.

Faces are simultaneously the thing that cannot be backed up and the thing that is
only lost by choosing to wipe. The no-wipe-first ordering is therefore strictly
better for data preservation: it risks nothing that a later wipe wouldn't have
destroyed anyway.

## Sources

- This repo: sdkapp/server.go, sdkapp/jdocspinger.go, servers/jdocs/server.go,
  vector-cloud protobufs; docs/clear-user-data-investigation.md (companion doc)
- kercre123/unlocking-vector; digital-dream-labs/oskr-owners-manual
- anki/vector-python-sdk (faces.py, photos)
- vector.thedroidyouarelookingfor.info: DDL receivership (2026-07-11 post),
  "Is DDL faltering" (2023-07-25)
