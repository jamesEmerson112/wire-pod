# wpsetup.keriganc.com — "paired but nothing happens" investigation

Investigated 2026-07-14. Symptom: bot in recovery mode, Chrome's Web Bluetooth chooser
appears, Vector pairs successfully, but the page never advances past the
"PAIR WITH VECTOR" screen. DevTools console shows only browser-extension noise
(`runtime.lastError`, `ObjectMultiplex`, `MaxListenersExceededWarning` — all from
`contentscript.js` / extensions, NOT from the setup tool).

## What the tool is

wpsetup.keriganc.com runs kercre123's fork of Digital Dream Labs' vector-web-setup
(github.com/kercre123/vector-web-setup). There is also a companion launcher,
kercre123/vector-web-setup-bat, which starts Chrome with
`--disable-web-security --enable-experimental-web-platform-features --user-data-dir=<clean>`
— its existence signals that plain-Chrome pairing on Windows is known-flaky.

## Expected flow (what "working" looks like)

1. Bot on charger, recovery screen (`anki.com/v` / `ddl.io/v`).
2. **Double-press the backpack button — the face must change to `######`** (pairing mode).
   "Was in recovery a minute ago" is not enough; `######` must be showing at pair time.
3. Click PAIR WITH VECTOR → Chrome chooser → select `Vector XXXX` → Pair.
4. Over BLE, Vector sends an RTS handshake message declaring its protocol version.
   The page's `HandleHandshake(version)` (rts.js) instantiates the matching handler.
   The Bluetooth icon at the top of the page lights up at this moment.
5. The `######` on Vector's face becomes a real 6-digit PIN; the page shows a PIN box
   (`onReadyForPin` → `setPhase("containerEnterPin")`).
6. Enter PIN → status box → firmware OTA download with progress bar (bot shows cloud icon).
7. "Vector has disconnected" message = download finished; bot reboots.

## Root-cause analysis of the silent stall (verified in rts.js)

`HandleHandshake` in the site's rts.js supports **RTS versions 2–6 only**:

```js
switch(version) {
  case 6: ... case 5: ... case 4: ... case 3: ... case 2: ...
  default:
    // Unknown
    break;          // rtsHandler stays null
}
...
rtsHandler.onReadyForPin(...)   // TypeError if version was unsupported
```

After GATT connect the page **passively waits for the robot to speak first** — it
sends nothing until Vector transmits a 5-byte RTS version handshake. Additionally
(from the tool's source, `vectorBluetooth.js`/`main.js`): if the GATT link drops,
`gattserverdisconnected` → `handleDisconnected()` **silently resets the page to the
initial instructions screen** — no dialog, no console error, by design. Same symptom
documented verbatim in DDL vector-web-setup issue #11.

Silent-failure modes matching the observed symptom:

- **No handshake ever arrives.** GATT connects, but the bot isn't actually in pairing
  mode (face not showing `######`), so it never sends the handshake and the page waits
  forever. The double-press pairing window also TIMES OUT — after every failed
  attempt, reset: take the bot off the charger, lift up/down (or wait for `######` to
  clear), back on charger, double-press again so `######` shows, then immediately
  click PAIR. Most common cause (wire-pod issue #386: "Never mind! I had to double
  click his button!"; first question in #481; wiki Troubleshooting step 3).
- **Connected-then-dropped due to a stale Windows bond.** The chooser labeling the bot
  "Paired" is itself the clue: a fresh recovery-mode bot should appear as a NEW
  device. "Paired" = Windows kept an old bond whose keys the recovery bot no longer
  honors → link dies right after connect → silent reset to instructions screen.
  Chrome on Windows leans on OS pairing state for Web Bluetooth. Fix: remove Vector
  in Windows Settings → Bluetooth & devices, optionally clear
  chrome://bluetooth-internals cache, retry.
- **Handshake arrives with RTS v7** (firmware ≥ 2.0.1.6090, issue #490). Falls into
  `default` → `rtsHandler` null → **visible** `TypeError: ... onReadyForPin` in the
  console (issue #515: "onReadyForPin null" on OSKR 2.0.1.6091, unsupported —
  downgrade needed). Only occurs when the bot is NOT in true recovery mode or is OSKR
  on 2.0.1.6090+. A clean console during the attempt argues against this mode.

Diagnostic split: page silently returns to instructions screen with clean console →
mode/bond problem (first two). TypeError after selecting the bot → RTS v7 (firmware
problem). The PIN appears on Vector's face only after a successful handshake — if no
digits ever appear, the BLE session never started, regardless of what the chooser
said ("Paired" is Windows' cached bond state, not proof of a live session).

## Prioritized fixes (community-verified)

1. **Double-press again so the face shows `######` at the moment you click Pair.**
   (issues #386, #481; wiki Troubleshooting) — most frequently reported fix.
2. If firmware is recent: make sure the bot truly booted into **recovery** (hold
   button ~15 s on charger), not just rebooted; avoids the RTS v7 handshake (#490).
3. `chrome://flags` → **Enable experimental web platform features** → relaunch
   (wiki; the maintainer's own .bat forces this flag plus a clean profile).
4. **Incognito window** (recommended by the tool's own error dialog and the wiki) —
   also rules out extensions.
5. Remove Vector from **Windows Settings → Bluetooth & devices** if listed
   (stale bond; the tool's own error text mentions needing a fresh bluetooth ID).
6. Toggle Windows Bluetooth off/on; hard reload the page (Ctrl+F5); retry several
   times — chronic Windows Web Bluetooth flakiness (DDL vector-web-setup issue #11).
7. Try **Edge**; or Chrome on an Android phone (works fine — wpsetup is independent
   of where wire-pod runs). Safari / Samsung Internet do NOT work (no Web Bluetooth).
8. Move the bot within ~1 m of the Bluetooth antenna (USB BLE dongles are notably
   worse than built-in radios — DDL issues #11, #37).
9. Alternate community mirror if the site itself misbehaves:
   https://vector.techshop82.com/html/main.html (issues #325, #447).
10. If `######` never appears no matter what: weak battery can prevent pairing mode
    (vectorrobot.info troubleshooting).

## Site outage history

Single documented outage 2024-05-04 (issue #325), fixed same day. No 2025–2026
downtime reports; recent problems are protocol-compatibility bugs (#481, #490, #515),
not availability. OTA firmware files are fetched (proxied from archive.org) during
the LATER authentication/download step — a firmware-CDN problem cannot explain a
stall at the PAIR stage. No Chrome 148–150 Web Bluetooth regression is documented;
the tool's deprecated `writeValue()` call still works (warning only, per #481 and
caniuse through Chrome 152).

## Vector 1.0 vs 2.0

No difference in this flow. The RTS v7 pitfall is firmware-version-, not
hardware-revision-dependent.
