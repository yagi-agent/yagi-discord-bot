# yagi-discord-bot

A Discord bot wrapper for [yagi](https://github.com/yagi-agent/yagi).

Directly imports yagi's `engine` package as a Go library — no subprocess required.

## Setup

### 1. Build

```bash
go build -o yagi-discord-bot
```

### 2. Configure profile

Clone [yagi-profiles](https://github.com/yagi-agent/yagi-profiles) into the data directory:

```bash
git clone https://github.com/yagi-agent/yagi-profiles ~/.config/yagi-discord-bot
```

This provides `IDENTITY.md` and other configuration files. The bot reads `IDENTITY.md` from the data directory as the system prompt.

### 3. Run

```bash
export DISCORD_BOT_TOKEN="your-token-here"
./yagi-discord-bot -model openai/gpt-4.1-nano
```

## Data Directory

Default: `~/.config/yagi-discord-bot/`

```
~/.config/yagi-discord-bot/
├── IDENTITY.md          # System prompt (from yagi-profiles)
├── sessions/            # Per-user conversation history
│   └── <hash>.json
└── memory/              # Per-user learned information
    └── <userID>.json
```

## Options

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `-token` | `DISCORD_BOT_TOKEN` | | Discord bot token (required) |
| `-model` | `YAGI_MODEL` | `openai/gpt-4.1-nano` | Provider/model |
| `-key` | | | API key (overrides env var) |
| `-prefix` | | `!` | Command prefix |
| `-identity` | | `<data>/IDENTITY.md` | Path to identity file |
| `-data` | | `~/.config/yagi-discord-bot` | Data directory |

## Trigger

The bot responds to:

- Mentions (`@yagi hello`)
- Prefixed messages (`!hello`)

## Required Discord Bot Intents

- Message Content Intent (enable in the Discord Developer Portal)
