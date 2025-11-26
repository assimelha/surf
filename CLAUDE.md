# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`surf` is a self-contained CLI tool written in Go that converts web pages to markdown for LLM consumption. It uses a headless Chromium browser (auto-downloaded on first run) with chromedp (Chrome DevTools Protocol) for full JavaScript execution and browser automation.

## Build Commands

```bash
make              # Build for current platform (creates ./surf)
make build        # Build all platforms (darwin-arm64, darwin-amd64, linux-amd64)
make test         # Build all platforms and run tests (300s timeout)
make clean        # Remove build artifacts
```

## Running Tests

```bash
go test -v -timeout=300s          # Run all tests
go test -v -run TestSpecificName  # Run a single test
```

Tests require Chromium to be available (auto-downloaded to `~/.surf/` on first run).

## Architecture

This is a single-file Go application (`main.go`) with approximately 650 lines of code. Key components:

- **Auto-downloading browser**: On first run, downloads Chromium to `~/.surf/`
- **Chrome DevTools Protocol**: Uses `github.com/chromedp/chromedp` for direct browser communication (no separate driver needed)
- **HTML to markdown conversion**: Uses `github.com/jaytaylor/html2text`
- **Phoenix LiveView support**: Auto-detects LiveView pages via `[data-phx-session]` and handles `.phx-connected` states, navigation events, and form submissions

### Key Functions

- `processRequest()` - Main orchestrator: sets up chromedp context, handles navigation, forms, JS execution, screenshots
- `handleForm()` - Form filling and submission with LiveView-aware logic
- `ensureChromium()` - Auto-download Chromium browser
- `waitForSelector()` - Browser wait utility using chromedp.WaitVisible
- `cleanMarkdown()` - Post-processing for markdown output

### Session Profiles

Profiles are stored in `~/.surf/profiles/<name>/` to maintain cookies and authentication across runs.
