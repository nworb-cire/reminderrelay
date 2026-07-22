# ReminderRelay justfile
# Run `just --list` to see all available recipes.

set shell := ["zsh", "-cu"]

binary := "reminderrelay"
home_dir := env('HOME')
app_dir := home_dir / "Applications/ReminderRelay.app"
install_bin := app_dir / "Contents/MacOS" / binary
plist_name := "com.github.njoerd114.reminderrelay"
plist_src := "deployment/" + plist_name + ".plist"
plist_dest := env('HOME') / "Library/LaunchAgents/" + plist_name + ".plist"
state_db := env('HOME') / ".local/share/reminderrelay/state.db"
info_plist := justfile_directory() / "internal/setup/app_info.plist"
bundle_id := "com.github.njoerd114.reminderrelay"
sign_id := env_var_or_default('REMINDERRELAY_CODESIGN_IDENTITY', '-')

# List available recipes
default:
    @just --list

# ── Build ──────────────────────────────────────────────────────────────────────

# Build the binary
build:
    CGO_ENABLED=1 go build -ldflags "-extldflags '-Wl,-sectcreate,__TEXT,__info_plist,{{ info_plist }}'" -o {{ binary }} ./cmd/reminderrelay
    codesign --force --sign - --identifier {{ bundle_id }} {{ binary }}

# Build with race detector (slower, for CI)
build-race:
    CGO_ENABLED=1 go build -race -ldflags "-extldflags '-Wl,-sectcreate,__TEXT,__info_plist,{{ info_plist }}'" -o {{ binary }} ./cmd/reminderrelay
    codesign --force --sign - --identifier {{ bundle_id }} {{ binary }}

# ── Run ────────────────────────────────────────────────────────────────────────

# Start the daemon (blocking)
run: build
    ./{{ binary }} daemon

# Run a single sync cycle and exit
sync-once: build
    ./{{ binary }} sync-once

# ── Test & Lint ────────────────────────────────────────────────────────────────

# Run unit tests with race detector
test:
    CGO_ENABLED=1 go test -race ./...

# Run integration tests (requires local Reminders + HA instance)
test-integration:
    CGO_ENABLED=1 go test -race -tags integration ./...

# Run linter
lint:
    golangci-lint run ./...

# Run linter and auto-fix where possible
lint-fix:
    golangci-lint run --fix ./...

# Format all Go source files
fmt:
    gofmt -w .

# ── Install / Uninstall ────────────────────────────────────────────────────────

# Build and install binary + launchd plist
install: build
    @echo "Installing binary to {{ install_bin }}..."
    mkdir -p '{{ app_dir }}/Contents/MacOS'
    install -m 755 {{ binary }} '{{ install_bin }}'
    install -m 644 '{{ info_plist }}' '{{ app_dir }}/Contents/Info.plist'
    codesign --force --deep --options runtime --timestamp=none --sign '{{ sign_id }}' '{{ app_dir }}'
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f '{{ app_dir }}'
    @echo "Installing launchd plist to {{ plist_dest }}..."
    mkdir -p $(dirname {{ plist_dest }})
    sed 's|__HOME__|{{ home_dir }}|g' {{ plist_src }} > {{ plist_dest }}
    -launchctl unload {{ plist_dest }}
    launchctl load {{ plist_dest }}
    @echo "Done. ReminderRelay will start on next login (RunAtLoad=true means it starts now too)."

# Unload plist and remove binary
uninstall:
    @echo "Unloading launchd plist..."
    -launchctl unload {{ plist_dest }}
    @echo "Removing plist..."
    -rm {{ plist_dest }}
    @echo "Removing binary..."
    -rm -rf '{{ app_dir }}'
    @echo "Done."

# ── Database ───────────────────────────────────────────────────────────────────

# Open an interactive SQLite shell against the state database
db:
    sqlite3 {{ state_db }}

# Dump the sync_items table in a readable format
db-dump:
    sqlite3 -column -header {{ state_db }} "SELECT * FROM sync_items ORDER BY last_synced_at DESC;"

# ── Maintenance ────────────────────────────────────────────────────────────────

# Remove build artifacts
clean:
    -rm -f {{ binary }}

# Download and tidy Go module dependencies
tidy:
    go mod tidy

# Print tool versions (useful for debugging CI)
versions:
    @go version
    @just --version
    @golangci-lint --version
    @sqlite3 --version
