# Why the wiki insists on "clear user data" — investigation results

Investigated 2026-07-14. Claim under investigation (Installation wiki): after the
wpsetup firmware update, "clear your bot's user data. Your bot can technically work
with WirePod if you don't, but weird behavior WILL happen."

## Verdict

The claim is well-founded. The maintainer has explicitly defined the "weird behavior,"
the mechanism is visible in both halves of this repo, and DDL's own (now-dead) official
docs required the same step for their Escape Pod product. It is, however, somewhat
overstated for modern wire-pod: sign-in without clearing is officially supported since
the Nov 2022 update — the risk is degraded/flaky auth and settings sync, not total failure.

## The maintainer's own definition of "weird behavior"

wire-pod issue #379 (SDK failing to download certs after fresh install), kercre123:
"This means you didn't clear user data before setting up wire-pod or there was some
problem during authentication. **This is the 'weird behavior' that was described.**"
Reporter confirmed clearing user data fixed it.

## What "clear user data" wipes (the bot's /data partition)

From vector-cloud/ (the code that runs on the robot):

- Cloud session JWT: `/data/data/com.anki.victor/persistent/token`
  (`internal/token/identity/getcert_vicos.go:14`)
- App/SDK client-token hashes: `/data/vic-gateway/token-hashes.json`
  (`gateway/tokens.go:27`) — every SDK/app connection to the robot is validated
  against these (`gateway/config_linux.go:44` → `CheckToken` → `CompareHashAndToken`)
- The robot's API cert `/data/vic-gateway/gateway.cert`
- Local jdocs copies (vic.AppTokens, vic.RobotSettings, account/entitlement docs)
  with DocVersion counters accumulated over years on Anki/DDL cloud
- Owner-association/onboarding state, faces/photos, WiFi credentials

## Mechanism of the weird behavior

1. **Version-conflicted token sync.** wire-pod issues a fresh GUID and stores its hash
   in a new vic.AppTokens jdoc (DocVersion 1). An uncleared bot holds an Anki-era
   AppTokens doc with a much higher DocVersion. Neither side does real conflict
   resolution — wire-pod's `vars.AddJdoc` (chipper/pkg/vars/vars.go:346) blindly
   overwrites its copy, while the robot's protocol expects proper version negotiation
   (REJECTED_BAD_DOC_VERSION, vector-cloud/internal/jdocs/translate.go:98). The
   wire-pod-issued GUID can fail to land in the bot's token-hashes.json → SDK/app
   auth 401s, sdk_config.ini problems (issues #379, #138).
2. **Association short-circuit.** An uncleared bot believes it already has a primary
   owner and may never send AssociatePrimaryUser; wire-pod falls back to heuristics
   (IP/ESN matching, TokenHashStore) and ultimately a hardcoded shared "global GUID"
   (chipper/pkg/servers/jdocs/server.go:150, token/token.go:32) → intermittent,
   inconsistent auth. kercre123 (issues #39, #41): wire-pod lacks the factory signing
   keys, so its token protocol is a partial re-implementation — fragile against
   pre-existing robot state.
3. **Settings jdocs conflicts.** vic.RobotSettings version mismatches → volume, eye
   color, timezone, temp units silently reverting (issues #239, #219, #261, #304, #321).
4. Code-level acknowledgments: wire-pod deletes its own stored jdocs for a bot on
   fresh association (`vars.DeleteData` at jdocs/server.go:72), and the token server
   logs "Clear your bot's userdata and try authenticating again" as a remedy
   (token/token.go:175).

Why it "technically works" anyway: wire-pod never verifies JWT signatures
(CreateJWT signs with a throwaway per-request RSA key; no service validates tokens),
so voice commands flow even with a stale Anki session token.

## Official DDL precedent (Wayback Machine; support site is dead)

- "Performing a Clear User Data Reset" (article 384): official generic fix for
  "persistent issues with Vector that have no other solution."
- Escape Pod onboarding guides (articles 349, 360): clearing user data was a
  **mandatory step** when repointing a bot at a self-hosted server — the exact
  operation wire-pod performs.

## Nuances / contradicting evidence

- The wiki's own Home page: "A robot can also sign in to wire-pod without needing to
  clear user data" — true since the Nov 2022 jdocs/token update. The Installation
  page's "technically work" hedge reflects this.
- Not every misbehavior is stale user data: issue #491's persistent command failures
  survived multiple data clears and turned out to be a faulty microphone.
- Distinct operation, don't conflate: the wiki's Things-to-Know page describes
  deleting **wire-pod's server-side** data (/etc/wire-pod/...) — different data clear,
  different failure class (update problems, bots repeatedly deauthenticating,
  switching between Escape Pod and IP configs).

## No-wipe migration: severity & recoverability (follow-up research, 2026-07-14)

- **Procedure**: there is no separate "keep data" flow — it's the same Authenticate
  flow in the wire-pod web UI; skipping the wipe just omits the on-robot menu step.
  No-wipe sign-in is a first-class feature (wiki Home page) since the Nov 2022
  update. Caveats: the old Vector mobile app cannot be used with a wire-pod bot at
  all (wire-pod's web UI replaces it), and production bots must be on EP firmware
  based on 1.8 or 2.0.1.
- **Severity**: narrow, recognized failure class — SDK auth/cert bootstrapping
  (#379: SDK cert download fails; #138: sdk_config.ini never created). Most wire-pod
  issues are pairing/network problems unrelated to kept data. At least one detailed
  community success story of a production bot migrated with data kept and working
  well (thedroidyouarelookingfor.info, 2022-08-18).
- **Settings-revert risk (#239)** is from a firmware *downgrade* case, still
  unresolved; the 1.7↔1.8 jdocs schema change (custom eye colors added in 1.8) is
  the suspected but unproven cause. A stock-1.7 bot moving to 1.8.x-ep with kept
  data crosses that boundary → modest added risk for settings persistence only
  (not faces/photos).
- **Recoverability: fully confirmed.** No-wipe → weirdness → clear user data +
  delete wire-pod's server-side data (Things-to-Know) + re-Authenticate = fixed
  (#379 reporter confirmed). No reports of lasting damage from trying no-wipe first.
- **What wpsetup flashes**: production bots land on 1.8.1.6051ep (field evidence:
  issue #47 — bot went "V0.9.0" → "V1.8.1.6051" right after setup; the 0.9.0
  reading is the recovery/factory partition version). 2.0.1.6076ep exists as the
  local OTA in wire-pod's inbuilt BLE setup.
- Research caveat: Reddit was unreachable during this pass; frequency estimates
  rest on GitHub issues + community blog.

## Sources

- Issues: kercre123/wire-pod #379, #138, #239, #219, #261, #304, #321, #288, #458,
  #39, #41, #491, #386
- DDL KB via Wayback: articles 384, 349, 360 (support.digitaldreamlabs.com)
- Protocol docs: randym32.github.io/Anki.Vector.Documentation (JDocs, Token Manager,
  "How to re-authenticate SDK apps")
- Blog: vector.thedroidyouarelookingfor.info — "Major Wire-Pod Update" (2022-11-29),
  "Testing Escape Pod 1.8.2" (2022-03-31)
- Code: chipper/pkg/servers/token/token.go, chipper/pkg/servers/jdocs/server.go,
  chipper/pkg/vars/vars.go, vector-cloud/gateway/tokens.go,
  vector-cloud/internal/token/identity/getcert_vicos.go,
  vector-cloud/internal/jdocs/translate.go
