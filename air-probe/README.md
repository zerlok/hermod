# air-probe — record how Air starts and ends sessions, with no agent

Two recorders, Python 3 stdlib only. Nothing calls a model: every prompt gets "probe: got it".

- `fake-claude` stands in for `claude`. It logs argv, env, process ids, tty state, every stdin line, stdin EOF,
  every signal, and its exit. In chat mode it speaks enough stream-json for the ACP adapter; in terminal mode it
  echoes lines and posts Air's `SessionStart` / `UserPromptSubmit` / `Stop` / `SessionEnd` hooks.
- `acp-tap` wraps the ACP adapter and logs every ACP message between Air and it, EOF on both sides and signals.

Logs: `~/.cache/air-probe/<time>-<pid>-{claude,acp}.log` (JSON lines, mode 0600: they hold prompts and Air's MCP
token). `fake-claude` writes a minimal transcript to `~/.claude/projects/<cwd>/<uuid>.jsonl` so Air lists the
session; `AIR_PROBE_TRANSCRIPT=0` turns that off. Use a throwaway project directory.

## Setup

```bash
mkdir -p ~/air-probe && cp fake-claude acp-tap ~/air-probe/ && chmod +x ~/air-probe/*
npm install -g --omit=optional @agentclientprotocol/claude-agent-acp@0.101.0   # no bundled Claude
```

**Chat mode** — `~/.jetbrains/acp.json` (absolute paths; Air does not load your shell):

```json
{
  "agent_servers": {
    "Claude (probe)": {
      "command": "/usr/bin/python3",
      "args": ["/home/<you>/air-probe/acp-tap", "<node ≥ 22>/bin/node", "<node ≥ 22>/bin/claude-agent-acp"],
      "env": { "CLAUDE_CODE_EXECUTABLE": "/home/<you>/air-probe/fake-claude" }
    }
  }
}
```

**Terminal mode** — point Air's Claude executable at `~/air-probe/fake-claude`.

Add `"AIR_PROBE_IGNORE_TERM": "1"` to `env` for a second pass: the fake then survives SIGTERM, which shows whether
and when SIGKILL follows (SIGKILL itself cannot be logged; the log just stops without an `exit`).

## Scenarios

Run each in a fresh chat, note the wall-clock time, then read the logs.

| #  | Action in Air                                   | Mode  |
|----|-------------------------------------------------|-------|
| 1  | New chat, send one prompt                       | both  |
| 2  | Close the chat tab                              | both  |
| 3  | Reopen the closed chat from the session list    | both  |
| 4  | Mark the chat done                              | both  |
| 5  | Delete the chat                                 | both  |
| 6  | Quit the IDE with a chat open                   | both  |
| 7  | Two chats open at once, close one               | chat  |
| 8  | Press stop right after sending (the fake answers instantly, so this mostly shows the cancel message) | chat |
| 9  | Leave the IDE idle a while: does Air start a headless `-p` call on its own? | — |

Quick read:

```bash
cd ~/.cache/air-probe
grep -h '"ev": "msg"' *-acp.log | python3 -c 'import sys,json;[print(e["t"],e["dir"],e.get("method") or "result#%s"%e.get("id")) for e in map(json.loads,sys.stdin)]'
grep -hE '"ev": "(start|signal|stdin_eof|exit|hook)"' *-claude.log | cut -c1-200
```
