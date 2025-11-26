# surf - shell command for simple LLM web browsing

> Fork of [chrismccord/web](https://github.com/chrismccord/web)

Shell-based web browser for LLMs that converts web pages to markdown, executes js, and interacts with pages.

```bash
# Convert a webpage to markdown
surf https://example.com

# Take a screenshot while scraping
surf https://example.com --screenshot page.png

# Execute JavaScript and capture log output along with markdown content
surf https://example.com --js "console.log(document.title)"

# Fill and submit a form
surf https://login.example.com \
    --form "login_form" \
    --input "username" --value "myuser" \
    --input "password" --value "mypass"
```

## Features

- **Self-contained executable** - Single native Go binary with no runtime dependencies
- **Markdown conversion** - HTML to markdown conversion for optimized consumption by LLMs
- **JavaScript execution** - Full browser engine with arbitrary js execution and console log capture
- **Complete logging** - Captures console.log/warn/error/info/debug and browser errors (JS errors, network errors, etc.)
- **Phoenix LiveView support** - Detects and properly handles Phoenix LiveView applications
- **Screenshots** - Save full-page screenshots
- **Form filling** - Automated form interaction with LiveView-aware submissions
- **Session persistence** - Maintains cookies and authentication across runs with profiles
- **Headful mode** - Run with visible browser window for debugging

## Installation

### Homebrew (macOS)

```bash
brew tap assimelha/tap
brew install surf
```

### Build from source

```bash
make              # Build ./surf for your platform
./surf https://example.com
```

You can then `sudo cp surf /usr/local/bin` to make it available system wide

### Multi-platform Build

For releases or deployment to other systems:
```bash
make build        # Build all platforms
```
This creates:
- `surf-darwin-arm64` - macOS Apple Silicon (M1/M2/M3)
- `surf-darwin-amd64` - macOS Intel
- `surf-linux-amd64` - Linux x86_64

## Usage Examples

```bash
# Basic scraping
surf https://example.com

# Output raw HTML
surf https://example.com --raw > output.html

# With truncation and screenshot
surf example.com --screenshot screenshot.png --truncate-after 123

# Run with visible browser window
surf https://example.com --headful --window-size 1920x1080

# Form submission with Phoenix LiveView support
surf http://localhost:4000/users/log-in \
    --form "login_form" \
    --input "user[email]" --value "foo@bar" \
    --input "user[password]" --value "secret" \
    --after-submit "http://localhost:4000/authd/page"

# Execute JavaScript on the page
surf example.com --js "document.querySelector('button').click()"

# Use named session profile
./surf --profile "mysite" https://authenticated-site.com
```

## Options

```
Usage: surf <url> [options]

Options:
  --help                     Show this help message
  --raw                      Output raw page instead of converting to markdown
  --truncate-after <number>  Truncate output after <number> characters and append a notice (default: 100000)
  --screenshot <filepath>    Take a screenshot of the page and save it to the given filepath
  --form <id>                The id of the form for inputs
  --input <name>             Specify the name attribute for a form input field
  --value <value>            Provide the value to fill for the last --input field
  --after-submit <url>       After form submission and navigation, load this URL before converting to markdown
  --js <code>                Execute JavaScript code on the page after it loads
  --profile <name>           Use or create named session profile (default: "default")
  --headful                  Run browser in visible window mode (not headless)
  --window-size <WxH>        Set browser window size (e.g., 1280x720), useful with --headful
```

## Phoenix LiveView Support

This tool has special support for Phoenix LiveView applications:

- **Auto-detection** - Automatically detects LiveView pages via `[data-phx-session]` attribute
- **Connection waiting** - Waits for `.phx-connected` class before proceeding
- **Form handling** - Properly handles LiveView form submissions with loading states
- **State management** - Waits for `.phx-change-loading` and `.phx-submit-loading` to complete

## System Requirements

- **Linux x64 or macOS** (Ubuntu 18.04+, RHEL 7+, Debian 9+, Arch Linux, macOS 10.12+)
- **~150MB free space** (for Chromium on first run)

### Linux System Packages

On Linux, you may need to install system packages for Chromium:

```bash
# Ubuntu/Debian - Core packages for Chromium
sudo apt install libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libasound2
```


## Testing

```bash
make test
```

This will build binaries for all platforms and run tests

## Development

### Available Commands

```bash
make              # Build for current platform (./surf)
make build        # Build all platforms (darwin-arm64, darwin-amd64, linux-amd64)
make test         # Build and run tests
make clean        # Remove build artifacts
```

### Build Requirements

- **Go 1.21+** (for building only)

## Architecture

- **Single Go binary with standalone headless Chromium download on first run**
- **Auto-download on first run** - Chromium downloaded to `~/.surf/`
- **Self-contained directory structure**:
  - `~/.surf/chromium/` - Headless Chromium browser
  - `~/.surf/profiles/` - Isolated session profiles for persistence
- **Cross-platform** - Builds for macOS (Intel/ARM64) and Linux x86_64
- **Chrome DevTools Protocol** - Uses chromedp for direct browser communication (no separate driver needed)

## License

MIT License
