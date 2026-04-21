# OCG-api-key-scraper

```
  +══════════════════════════════════════════════════════════+
  |  ██╗  ██╗███████╗██╗   ██╗ ██████╗██╗  ██╗██╗  ██╗    |
  |  ██║ ██╔╝██╔════╝╚██╗ ██╔╝██╔════╝██║  ██║██║ ██╔╝    |
  |  █████╔╝ █████╗   ╚████╔╝ ██║     ███████║█████╔╝      |
  |  ██╔═██╗ ██╔══╝    ╚██╔╝  ██║     ██╔══██║██╔═██╗      |
  |  ██║  ██╗███████╗   ██║   ╚██████╗██║  ██║██║  ██╗     |
  |  ╚═╝  ╚═╝╚══════╝   ╚═╝    ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝    |
  |                                                          |
  |  API Key Scraper + Validator   no credits · no service  |
  +══════════════════════════════════════════════════════════+
```

Scans GitHub for leaked OpenAI, Anthropic, Gemini API keys. Tests each one live. Saves the working ones to disk.

No credits. No cloud service. No subscription. Just Go.

---

## 🔍 What it does

You give it a GitHub token. It runs targeted code-search queries across public repositories looking for exposed API keys. Every match gets regex-validated against the real key format for that provider. If you enable live validation, it hits each provider's API directly to confirm the key is active.

Results are split into three tiers:

| Status | Meaning |
|--------|---------|
| 🟢 **LIVE** | Key is valid right now — use it |
| 🟡 **warm** | Key is real but daily quota is exhausted — retry tomorrow |
| 🔴 **dead** | Key is invalid or revoked |

Each tier gets its own output file per provider.

---

## 🎯 Providers

| Provider | Key prefix | Queries run |
|----------|------------|------------|
| OpenAI | `sk-` / `sk-proj-` | 10 |
| Anthropic | `sk-ant-` | 8 |
| Google Gemini | `AIza` | 10 |

Every provider gets its own set of search queries — `.env` files, config files, Python scripts, JavaScript files, Go files, YAML files. The search is sorted by most recently indexed first so you hit the freshest results before anyone else does.

---

## ⚙️ Features

**Smart deduplication** — Every key found is tracked in `keychk_seen.txt`. Run the tool twice and it skips every key it already found. No duplicates ever hit the output files.

**Token rotation** — Load as many GitHub PATs as you want. The tool cycles through them automatically to stay within GitHub's rate limits. No manual switching.

**Freshness filter** — Optionally restrict results to files committed within the last N days. Useful for finding keys that were just pushed and haven't been revoked yet. Default is off because abandoned old repos often still hold live keys.

**Page start** — Skip the first N pages of GitHub search results. Early pages get burned fast by other scrapers. Starting from page 3 or 4 often surfaces keys nobody else has seen yet.

**Live validation** — Off by default. Turn it on to hit each provider's API during the scan. The tool knows each provider's test endpoint so it doesn't waste real quota — it uses free check calls where available.

**Concurrent workers** — Configurable. Default is 10 parallel goroutines. Crank it up for faster scans or dial it down if you're being careful with rate limits.

**Token management** — Full CRUD from the menu. Add tokens, view them masked, remove by number, test a specific token against the GitHub API before using it.

**Zero external dependencies at runtime** — Two Go packages: color output plus a progress bar. Everything else is stdlib.

---

## 📁 Output files

All files land in your configured output directory (default: current folder).

| File | Contents |
|------|---------|
| `openai_live.txt` | Confirmed working OpenAI keys |
| `openai_warm.txt` | Valid OpenAI keys with exhausted quota |
| `openai_all.txt` | Full log of every key found |
| `anthropic_live.txt` | Confirmed working Anthropic keys |
| `gemini_live.txt` | Confirmed working Gemini keys |
| `keychk_seen.txt` | Cache of every key ever found — prevents re-reporting |

Each entry in the output files includes the full key, the repo it came from, the exact file path, the GitHub URL, and the timestamp it was found.

---

## 🔑 GitHub tokens

You need at least one GitHub Personal Access Token. The GitHub code search API requires authentication. Without a token the tool will not run.

Get one at **github.com → Settings → Developer settings → Personal access tokens**. The only scope needed is `public_repo` for read access to public code.

Tokens are stored in `.env-git-keys` in the working directory. The format is one token per line:

```
ghp_yourtoken1here
GITHUB_TOKEN=ghp_yourtoken2here
```

Both formats are accepted. Comments starting with `#` are ignored.

---

## 🔧 Build

Requires Go 1.21 or newer.

```bash
git clone https://github.com/Krainium/OCG-api-key-scraper.git
cd OCG-api-key-scraper
go build -o keychk keychk.go
```

---

## 🚀 Usage

```bash
./keychk
```

The interactive menu opens immediately. Everything is configurable from there — providers, pages, workers, validation, output directory. No flags required.

**The menu at a glance:**

```
[1] Providers        — choose openai / anthropic / gemini
[2] Pages / query    — how many GitHub result pages per query
[3] Workers          — parallel goroutines
[4] Validate keys    — hit the API live to confirm each key
[5] Output dir       — where files are saved
[6] Cache file       — path to the seen-keys file
[P] Page start       — skip burned early pages
[F] Freshness filter — only scan recently committed files
[7] Add token        — paste a GitHub PAT
[8] View tokens      — masked display of loaded tokens
[9] Remove token     — remove by number
[T] Test a token     — verify it hits the GitHub API
[C] Clear seen cache — start fresh on next run
[R] Run scan         — start
[Q] Quit
```

---

## 🧠 How the search works

The tool fires search queries against GitHub's code search API using `sort=indexed&order=desc`. That sort order puts the most recently indexed files at the top — meaning files that just got pushed are at position 1. Each query targets specific file types like `.env` files or language-filtered results in Python or JavaScript. The queries are spaced 7 seconds apart to stay within GitHub's 10-request-per-minute limit on code search.

Once it has a list of files, it fetches the raw content from `raw.githubusercontent.com` first. If that fails, it falls back to the GitHub Contents API. Files over 500 KB are skipped. Files inside `node_modules`, `venv`, `.git`, or build directories are skipped.

Every key match goes through the regex for that provider before it's accepted. Nothing gets saved unless the format is right.

---

## 📊 Scan output

While running, each found key prints immediately:

```
  OPENAI     ● LIVE   [14:22:05]   sk-proj-abc123…       user/leaked-config/  .env
  ANTHROPIC  ● warm   [14:22:11]   sk-ant-api01-xy…      dev/old-project/     config.py
  GEMINI     ● dead   [14:22:19]   AIzaSyXXXXXXXXX…      org/demo-app/        app.js
```

A progress bar tracks files scanned. A live counter shows total keys found. Ctrl+C at any point saves all results found so far before exiting.

---

## ⚠️ Rate limits

GitHub code search allows 10 requests per minute per token. The tool enforces a 7-second gap between queries. When it hits a rate limit, it reads the `X-RateLimit-Reset` header and sleeps exactly until the reset time rather than sleeping a fixed duration.

Loading multiple GitHub tokens lets the tool rotate between them and effectively multiply the throughput.
