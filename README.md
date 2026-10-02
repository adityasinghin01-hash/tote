# tote

[![test](https://github.com/adityasinghin01-hash/tote/actions/workflows/test.yml/badge.svg)](https://github.com/adityasinghin01-hash/tote/actions/workflows/test.yml)

**Take your AI to any computer — and prove it arrived with its memory intact.**

> Early preview (v0.1). Every push runs the whole journey — pack, send, open on a blank machine with a private AI install, quiz, send home, wipe — on Windows, Linux and macOS. Hands-on use so far has been on macOS.

You've taught your AI coding assistant a lot: your rules, your projects, where you stopped. Then you sit at a different computer — a lab PC, a friend's laptop — and it knows nothing. tote packs that knowledge on your machine and unpacks it on the other one with **one pasted command**.

```
# on your computer — your AI does this part
tote prepare --tool claude            # gathers memory, skills, settings, recent chats (squeezed)
# … your AI writes START-HERE.md, DONE.md and a short quiz, then:
tote send ~/tote-outbox/<folder>

# on the other computer — paste the line tote printed
curl -fsSL https://github.com/adityasinghin01-hash/tote/releases/latest/download/install.sh | sh -s -- tote1_…
```

## What makes it different

- **Proof, not hope.** The sending AI writes a quiz; tote locks the answers (only fingerprints travel). The receiving AI answers through `tote quiz` and **tote** grades it: `Context received: 9/10`. In testing, a fresh AI scored 10/10 on a full box and 8/10 when two notes were deleted — missing exactly the two questions those notes answered.
- **Small reads.** Chat logs are ~78% tool output. tote keeps your words and the AI's replies and drops the rest: 1.2 GB of real chats → 6.8 MB. The new AI starts from a ~10K-token index and opens details only when needed.
- **Safe on someone else's computer.** Guest mode looks before it touches, asks only real yes/no questions, keeps everything (including the AI's login) in one private folder, never touches the owner's files or login, and `tote leave` wipes it all — tote included.
- **Any AI.** Built-in support for Claude Code, Codex and OpenCode (Gemini CLI and Kimi from their docs, untested). For any other tool, the AI packs its own files and writes a description tote learns. Every box also carries `AGENTS.md`, read by 25+ AI tools.
- **Comes home.** `tote send-home` on the other computer, `tote merge` at home: only changes travel, nothing at home is overwritten if it changed there too, and the other computer's chats become resumable at home.

## Privacy and security

- Boxes are encrypted on your computer ([age](https://age-encryption.org), scrypt) with a 128-bit key in the ticket **plus** a 6-digit PIN you send a different way.
- Mailboxes only ever hold locked boxes: your own GitHub repo (release files up to 2 GB, swept hourly), your own S3/R2 bucket, or a shared folder.
- tote packs **only your own files** — never other users' — and never logins, keys or `.env` files. Real key formats are blanked everywhere; password-style values are blanked in notes and chats.
- The quiz stops an AI that skimmed. It does not stop one deliberately guessing short answers.

## Commands

```
tote prepare [--tool claude,codex,…] [--days 7]   gather your AI's files + chat summaries
tote send <folder>                                lock it and post it to your mailbox
tote get <ticket>                                 collect and unlock a box
tote guest <ticket>                               use your AI on someone else's computer, privately
tote run [tool] · tote leave · tote status
tote quiz [--answers file]                        the receiving AI proves it read the box
tote send-home · tote merge <ticket>              bring the work back
tote mailbox [use github|s3|folder …]             where boxes wait
```

Box format: [docs/SPEC.md](docs/SPEC.md). Build: Go 1.27 — `go build ./cmd/tote`. Every push runs the whole journey on Windows, Linux and macOS ([tools/e2e](tools/e2e/main.go)).

MIT licensed.
