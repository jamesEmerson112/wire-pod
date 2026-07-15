# The LLM "Knowledge Graph" feature — full investigation

Investigated 2026-07-15 via 3-agent fan-out (LLM core / config surface / pipeline trace).
"Knowledge Graph" is Anki's legacy name for Vector's Q&A voice feature; wire-pod backs it
with plain OpenAI-compatible chat completions (`ttr/kgsim.go` = "knowledge graph simulation").

## How it gets triggered (two paths)

1. **Explicit question** — saying "question", "I have a question", "conversation",
   "let's talk" (keyphrases: `chipper/intent-data/en-US.json:197-201`, intent
   `intent_knowledge_promptquestion`) puts the bot in ask-a-question mode; the robot then
   calls the dedicated `StreamingKnowledgeGraph` gRPC (`pkg/servers/chipper/knowledgegraph.go:12`
   → `preqs/knowledgegraph.go:101` → `ttr.StreamingKGSim(..., isKG=true)`).
2. **Intent-graph fallback** — any unmatched command ("what's the tallest mountain?")
   falls through to the LLM when `Knowledge.IntentGraph` is on
   (`preqs/intent_graph.go:62-88`). A placeholder `intent_greeting_hello` is sent to close
   the robot's request stream (`ttr/kgsim.go:385-390`); the real answer arrives separately.

**Who speaks:** for LLM providers, wire-pod itself opens an SDK connection back to the robot
(`kgsim.go:210`), takes BehaviorControl at OVERRIDE_BEHAVIORS priority (`bcontrol.go:84-141`),
and drives `SayText` — the server does the talking. (Houndify, by contrast, returns
`SpokenText` in the gRPC response and the robot's firmware speaks it natively.)

## The request that actually goes to OpenAI

Built in `CreateAIReq` (`ttr/kgsim.go:137-187`):

- **System prompt** default: `"You are a helpful, animated robot called Vector. Keep the
  response concise yet informative."` (`kgsim.go:138`); `OpenAIPrompt` replaces it verbatim
  (used for ALL providers, not just OpenAI). `CreatePrompt` (`kgsim_cmds.go:152-174`) appends
  STT-safety text plus the command grammar when CommandsEnable is on.
- **Model**: provider `openai` → **hardcoded `gpt-4o-mini`**, the `Model` config field is
  ignored (`kgsim.go:156`). Together → `Model` or default `meta-llama/Llama-3-70b-chat-hf`;
  custom → `Model` verbatim.
- **Hardcoded**: `MaxTokens: 2048`, `Temperature: 1`, `TopP: 1` (`kgsim.go:178-180`).
  Always `Stream: true`.
- On OpenAI "model does not exist" error: one auto-retry with GPT-3.5-Turbo
  (`kgsim.go:266-276`) + a (stale) log hint about funding the account.

## Streaming speech

`StreamingKGSim` (`kgsim.go:189-504`) chunks the token stream at sentence boundaries
(`...`, `.`, `?`, `!` — `kgsim.go:356-370`) and speaks each sentence as soon as it's complete
— the robot starts talking after the FIRST sentence, while the rest still generates.
Filler animation (`anim_knowledgegraph_searching_01`) plays until the first chunk lands.

**Barge-in interrupt**: `kgsim_interrupt.go:12-92` watches the wake word and the capacitive
touch sensor (baseline+50 for >5 samples) — saying "Hey Vector" or petting him stops the
LLM speech mid-response. Intentional design, not a bug.

## LLM commands (CommandsEnable)

Prompt teaches the model `{{command||parameter}}` syntax (`kgsim_cmds.go:152-174`).
Valid commands (`kgsim_cmds.go:105-141`):

| Command | Effect |
|---|---|
| `playAnimationWI` | non-blocking animation (happy, sad, angry, thinking, celebrate, love…) |
| `playAnimation` | blocking/interrupting animation, same 11 names → real anims via `animationMap` |
| `getImage` | **vision**: countdown 3-2-1, takes a camera photo, base64s it into a follow-up gpt-4o-mini request (`kgsim_cmds.go:393-614`) — "what do you see?" works |
| `newVoiceRequest` | reopens the mic for a follow-up turn = conversation mode (`kgsim_cmds.go:616-619`) |

`playSound` exists but is commented out and stubbed (never plays anything).

## Chat memory (SaveChat)

- Per-robot (ESN-keyed), **in-memory only**: `vars.RememberedChats` (`vars/vars.go:82-87`).
  Sliding window of 16 messages / 8 turns (`kgsim.go:46-66`). **Lost on every restart.**
- `openaiChats.json` is a ghost: `.gitignore`'d but no code reads or writes it anywhere.
- Cleared via web UI "Delete Saved Chats" → `/api/delete_chats`
  (`config-ws/webserver.go:288-291`).

## OpenAI TTS voice

Used only when STT language ≠ en-US, or `OpenAIVoiceWithEnglish` is checked
(`kgsim_cmds.go:293-296`). Default voice: **fable** (`kgsim_cmds.go:316-327`). PCM is
downsampled 24→16 kHz, volume-boosted, and streamed to the robot's speaker in 1024-byte
chunks (`kgsim_cmds.go:330-391`). Otherwise Vector's own voice speaks (`UseVectorVoice: true`).

## Config reality check (dead knobs)

| Knob | Reality |
|---|---|
| `Model` (openai provider) | **ignored** — always gpt-4o-mini (`kgsim.go:156`) |
| `Temperature`, `TopP` | defined in struct + API, **never read** — requests hardcode 1/1 |
| `RobotName` | never read anywhere in the codebase; UI always sends `""` |
| `MaxTokens` | not configurable, fixed 2048 |

Env seeding (first boot only): `KNOWLEDGE_ENABLED`, `KNOWLEDGE_PROVIDER`, `KNOWLEDGE_KEY`,
`KNOWLEDGE_ID` (`vars/config.go:59-83`). Everything else via web UI →
`POST /api/set_kg_api` (no server-side validation; `GET /api/get_kg_api` returns the
API key in plaintext — LAN-exposed).

## Providers

- **openai** — api.openai.com, hardcoded gpt-4o-mini.
- **together** — `https://api.together.xyz/v1`, default Llama-3-70b-chat-hf.
- **custom** — any OpenAI-compatible server via `Endpoint` (UI hint:
  `http://localhost:11434/v1` for Ollama, key "ollama") → fully local option.
- **houndify** — legacy non-LLM path (`preqs/knowledgegraph.go:37-99`), needs Client ID+Key;
  no prompt/memory/commands support. Still compiled in and functional.

## Bugs / sharp edges found (upstream, not ours)

- Unknown/empty `Provider` → nil `*openai.Client` → panic (no `default:` in switch,
  `kgsim.go:242-257`).
- Malformed LLM command without `||` → index-out-of-range panic
  (`kgsim_cmds.go:203`).
- `DoSayText` calls `removeSpecialCharacters` but discards the result
  (`kgsim_cmds.go:291`) — spoken text isn't actually sanitized there.
- `ActionNewRequest` and `ActionPlaySound` are both `= 4` (`kgsim_cmds.go:29-32`) —
  latent collision, masked because playSound is disabled.
- Together default model inconsistency: main path Llama-3, vision path Llama-2
  (`kgsim.go:245` vs `kgsim_cmds.go:472`); config load auto-migrates Llama-2 → Llama-3.
- Legacy `ProcessIntent` (pre-1.7 firmware) fallback ignores the houndify branch
  (`preqs/intent.go:45-57`).
- **No timeouts** anywhere (all `context.Background()`), no content filter, no rate limit.

## What matters for THIS setup (provider=openai, model empty, intentgraph on)

Effective knobs: `Key` (required), `IntentGraph` (on), `OpenAIPrompt` (personality),
`SaveChat` (conversation memory), `CommandsEnable` (animations + camera vision),
`OpenAIVoice`/`WithEnglish` (TTS swap). Model/TopP/Temperature boxes: no effect.
Cost profile: gpt-4o-mini, ≤2048 output tokens/request, + tiny vision calls when
`getImage` fires; TTS calls only if the OpenAI-voice option is enabled.
