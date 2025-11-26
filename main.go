package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/jaytaylor/html2text"
)

const DEFAULT_TRUNCATE_AFTER = 100000

type FormInput struct {
	Name  string
	Value string
}

type Config struct {
	URL            string
	Profile        string
	FormID         string
	Inputs         []FormInput
	AfterSubmitURL string
	JSCode         string
	ScreenshotPath string
	TruncateAfter  int
	RawFlag        bool
	Headful        bool
	WindowSize     string
}

func main() {
	config := parseArgs()

	if config.URL == "" {
		printHelp()
		os.Exit(1)
	}

	// Ensure Chromium is installed
	err := ensureChromium()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up Chromium: %v\n", err)
		os.Exit(1)
	}

	// Process the request
	result, err := processRequest(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error processing request: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result)
}

func getChromiumDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".surf")
}

func getChromiumExec() string {
	chromiumDir := getChromiumDir()
	switch goruntime.GOOS {
	case "darwin":
		// Playwright Chromium uses "Google Chrome for Testing.app" and architecture-specific directories
		archSuffix := ""
		if goruntime.GOARCH == "arm64" {
			archSuffix = "-arm64"
		}
		return filepath.Join(chromiumDir, "chromium", "chrome-mac"+archSuffix, "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case "linux":
		return filepath.Join(chromiumDir, "chromium", "chrome-linux", "chrome")
	default:
		return ""
	}
}

func ensureChromium() error {
	chromiumExec := getChromiumExec()
	if chromiumExec == "" {
		return fmt.Errorf("unsupported platform: %s", goruntime.GOOS)
	}

	// Check if Chromium executable exists
	if _, err := os.Stat(chromiumExec); err == nil {
		return nil
	}

	// Download and extract Chromium
	fmt.Println("Chromium not found, downloading...")

	var chromiumUrl string
	switch goruntime.GOOS {
	case "darwin":
		if goruntime.GOARCH == "arm64" {
			chromiumUrl = "https://playwright.azureedge.net/builds/chromium/1200/chromium-mac-arm64.zip"
		} else {
			chromiumUrl = "https://playwright.azureedge.net/builds/chromium/1200/chromium-mac.zip"
		}
	case "linux":
		chromiumUrl = "https://playwright.azureedge.net/builds/chromium/1200/chromium-linux.zip"
	}

	chromiumDir := getChromiumDir()
	err := downloadChromium(chromiumUrl, filepath.Join(chromiumDir, "chromium"))
	if err != nil {
		return fmt.Errorf("failed to download Chromium: %v", err)
	}

	// Verify the executable exists after download
	if _, err := os.Stat(chromiumExec); err != nil {
		return fmt.Errorf("Chromium executable not found after download: %s", chromiumExec)
	}

	fmt.Printf("Chromium downloaded to: %s\n", chromiumDir)
	return nil
}

func downloadChromium(url, destDir string) error {
	// Create destination directory
	err := os.MkdirAll(destDir, 0755)
	if err != nil {
		return fmt.Errorf("could not create directory %s: %v", destDir, err)
	}

	// Download the zip file
	fmt.Printf("Downloading Chromium from %s...\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("could not download Chromium: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Create temporary file
	tempFile, err := os.CreateTemp("", "chromium-*.zip")
	if err != nil {
		return fmt.Errorf("could not create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Copy download to temp file
	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		return fmt.Errorf("could not save download: %v", err)
	}

	tempFile.Close()

	// Extract the zip file
	fmt.Println("Extracting Chromium...")
	return extractZip(tempFile.Name(), destDir)
}

func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Create destination directory
	os.MkdirAll(dest, 0755)

	// Extract files
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}

		path := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.FileInfo().Mode())
			rc.Close()
			continue
		}

		// Create directories for file
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			rc.Close()
			return err
		}

		// Create the file
		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func processRequest(config Config) (string, error) {
	baseURL := ensureProtocol(config.URL)

	chromiumExec := getChromiumExec()
	chromiumDir := getChromiumDir()
	profileDir := filepath.Join(chromiumDir, "profiles", config.Profile)
	os.MkdirAll(profileDir, 0755)

	// Set up chromedp allocator options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromiumExec),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", !config.Headful),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-component-extensions-with-background-pages", true),
		chromedp.Flag("disable-default-apps", true),
	)

	// Add window size if specified
	if config.WindowSize != "" {
		opts = append(opts, chromedp.WindowSize(parseWindowSize(config.WindowSize)))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Set up timeout
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 60*time.Second)
	defer timeoutCancel()
	ctx = timeoutCtx

	// Console message capture
	var consoleMessages []string
	var consoleMu sync.Mutex

	// Listen for console events
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			consoleMu.Lock()
			defer consoleMu.Unlock()

			level := strings.ToUpper(string(ev.Type))
			if level == "LOG" {
				level = "LOG"
			} else if level == "WARNING" {
				level = "WARNING"
			} else if level == "ERROR" {
				level = "ERROR"
			}

			var msgParts []string
			for _, arg := range ev.Args {
				val := ""
				if arg.Value != nil {
					// Properly unmarshal JSON value
					var strVal string
					if err := json.Unmarshal(arg.Value, &strVal); err == nil {
						val = strVal
					} else {
						// Fallback: try as raw value (numbers, booleans, etc.)
						val = strings.Trim(string(arg.Value), "\"")
					}
				} else if arg.Description != "" {
					val = arg.Description
				}
				if val != "" {
					msgParts = append(msgParts, val)
				}
			}
			if len(msgParts) > 0 {
				consoleMessages = append(consoleMessages, fmt.Sprintf("[%s] %s", level, strings.Join(msgParts, " ")))
			}

		case *cdpruntime.EventExceptionThrown:
			consoleMu.Lock()
			defer consoleMu.Unlock()
			if ev.ExceptionDetails != nil {
				msg := ev.ExceptionDetails.Text
				if ev.ExceptionDetails.Exception != nil && ev.ExceptionDetails.Exception.Description != "" {
					msg = ev.ExceptionDetails.Exception.Description
				}
				consoleMessages = append(consoleMessages, fmt.Sprintf("[ERROR] %s", msg))
			}
		}
	})

	// Navigate to page
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL))
	if err != nil {
		return "", fmt.Errorf("could not navigate to %s: %v", baseURL, err)
	}

	// Wait for page to load
	err = chromedp.Run(ctx, chromedp.WaitReady("body"))
	if err != nil {
		return "", fmt.Errorf("page did not load: %v", err)
	}

	// Detect LiveView pages
	var isLiveView bool
	err = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('[data-phx-session]') !== null`, &isLiveView))
	if err != nil {
		isLiveView = false
	}

	if isLiveView {
		fmt.Println("Detected Phoenix LiveView page, waiting for connection...")
		// Wait for Phoenix LiveView to connect
		err = waitForSelector(ctx, ".phx-connected", 10*time.Second)
		if err != nil {
			fmt.Printf("Warning: Could not detect LiveView connection: %v\n", err)
		} else {
			fmt.Println("Phoenix LiveView connected")
		}
	}

	// Handle form submission if specified
	if config.FormID != "" && len(config.Inputs) > 0 {
		err = handleForm(ctx, config, isLiveView)
		if err != nil {
			return "", fmt.Errorf("error handling form: %v", err)
		}
	}

	// Execute JavaScript if provided
	if config.JSCode != "" {
		// Store current URL before executing JS
		var currentURL string
		chromedp.Run(ctx, chromedp.Location(&currentURL))

		var result interface{}
		err = chromedp.Run(ctx, chromedp.Evaluate(config.JSCode, &result))
		if err != nil {
			fmt.Printf("Warning: JavaScript execution failed: %v\n", err)
		}

		// Wait for navigation based on page type
		if isLiveView {
			fmt.Println("Waiting for Phoenix LiveView navigation...")
			time.Sleep(500 * time.Millisecond)

			var newURL string
			chromedp.Run(ctx, chromedp.Location(&newURL))
			if newURL != currentURL {
				fmt.Println("URL changed, waiting for page to stabilize...")
				time.Sleep(500 * time.Millisecond)
			} else {
				fmt.Println("Info: No navigation detected (in-place LiveView update)")
			}
		} else {
			fmt.Println("Waiting for page navigation...")
			time.Sleep(200 * time.Millisecond)

			var newURL string
			chromedp.Run(ctx, chromedp.Location(&newURL))

			if newURL != currentURL {
				fmt.Println("Navigation detected, waiting for page load...")
				err = chromedp.Run(ctx, chromedp.WaitReady("body"))
				if err != nil {
					fmt.Printf("Warning: Page load wait timed out: %v\n", err)
				} else {
					fmt.Println("Page load completed")
				}
			} else {
				fmt.Println("Info: No navigation detected (page update without URL change)")
			}
		}
	}

	// Take screenshot if requested
	if config.ScreenshotPath != "" {
		var screenshot []byte
		err = chromedp.Run(ctx, chromedp.FullScreenshot(&screenshot, 100))
		if err != nil {
			return "", fmt.Errorf("error taking screenshot: %v", err)
		}
		err = os.WriteFile(config.ScreenshotPath, screenshot, 0644)
		if err != nil {
			return "", fmt.Errorf("error saving screenshot: %v", err)
		}
		fmt.Printf("Screenshot saved to %s\n", config.ScreenshotPath)
	}

	// Navigate to after-submit URL if provided
	if config.AfterSubmitURL != "" {
		fmt.Printf("Navigating to after-submit URL: %s\n", config.AfterSubmitURL)
		err = chromedp.Run(ctx, chromedp.Navigate(config.AfterSubmitURL))
		if err != nil {
			return "", fmt.Errorf("could not navigate to after-submit URL: %v", err)
		}
		chromedp.Run(ctx, chromedp.WaitReady("body"))
	}

	// Get page content
	var content string
	err = chromedp.Run(ctx, chromedp.OuterHTML("html", &content))
	if err != nil {
		return "", fmt.Errorf("could not get page content: %v", err)
	}

	// Return raw HTML if requested
	if config.RawFlag {
		return content, nil
	}

	// Convert HTML to markdown
	text, err := html2text.FromString(content)
	if err != nil {
		return "", fmt.Errorf("could not convert HTML to text: %v", err)
	}

	// Clean and format the markdown
	markdown := cleanMarkdown(text)

	// Truncate if specified
	if len(markdown) > config.TruncateAfter {
		markdown = markdown[:config.TruncateAfter] + fmt.Sprintf("\n\n... (output truncated after %d chars, full content was %d chars)", config.TruncateAfter, len(text))
	}

	// Add header with URL and console messages
	result := fmt.Sprintf("==========================\n%s\n==========================\n\n%s", baseURL, markdown)

	// Add console messages if any
	consoleMu.Lock()
	if len(consoleMessages) > 0 {
		result += "\n\n" + strings.Repeat("=", 50) + "\nCONSOLE OUTPUT:\n" + strings.Repeat("=", 50) + "\n"
		for _, msg := range consoleMessages {
			result += msg + "\n"
		}
	}
	consoleMu.Unlock()

	// Navigate away to trigger localStorage flush before shutdown
	chromedp.Run(ctx, chromedp.Navigate("about:blank"))
	// Wait for navigation to complete
	time.Sleep(100 * time.Millisecond)

	// Explicitly cancel context to ensure browser shuts down
	timeoutCancel()
	cancel()
	// Wait for browser process to fully exit and flush data
	time.Sleep(500 * time.Millisecond)
	allocCancel()

	return result, nil
}

// waitForSelector waits for an element matching the selector to appear
func waitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return chromedp.Run(timeoutCtx, chromedp.WaitVisible(selector))
}

func handleForm(ctx context.Context, config Config, isLiveView bool) error {
	// Fill form inputs
	for _, input := range config.Inputs {
		selector := fmt.Sprintf("#%s input[name='%s']", config.FormID, input.Name)

		err := chromedp.Run(ctx,
			chromedp.WaitVisible(selector),
			chromedp.Clear(selector),
			chromedp.SendKeys(selector, input.Value),
		)
		if err != nil {
			return fmt.Errorf("could not fill input %s: %v", input.Name, err)
		}
	}

	formSelector := fmt.Sprintf("#%s", config.FormID)

	if isLiveView {
		// For LiveView, submit by pressing Enter
		fmt.Println("Waiting for Phoenix LiveView navigation...")
		err := chromedp.Run(ctx, chromedp.SendKeys(formSelector, "\r"))
		if err != nil {
			return fmt.Errorf("could not submit LiveView form: %v", err)
		}

		// Wait for LiveView to process
		time.Sleep(500 * time.Millisecond)
		fmt.Println("LiveView form submitted")
	} else {
		// For regular forms, try submit button first, then Enter
		submitSelector := fmt.Sprintf("#%s input[type='submit'], #%s button[type='submit']", config.FormID, config.FormID)

		var nodes []*cdpruntime.RemoteObject
		err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.querySelectorAll("%s").length`, submitSelector),
			&nodes,
		))

		var submitCount int
		chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.querySelectorAll("%s").length`, submitSelector),
			&submitCount,
		))

		if submitCount > 0 {
			err = chromedp.Run(ctx, chromedp.Click(submitSelector))
			if err != nil {
				return fmt.Errorf("could not click submit button: %v", err)
			}
		} else {
			err = chromedp.Run(ctx, chromedp.SendKeys(formSelector, "\r"))
			if err != nil {
				return fmt.Errorf("could not submit form: %v", err)
			}
		}
		fmt.Println("Form submitted")
	}

	return nil
}

func parseArgs() Config {
	config := Config{
		TruncateAfter: DEFAULT_TRUNCATE_AFTER,
		Profile:       "default",
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch arg {
		case "--help":
			printHelp()
			os.Exit(0)
		case "--raw":
			config.RawFlag = true
		case "--truncate-after":
			if i+1 < len(args) {
				val, err := strconv.Atoi(args[i+1])
				if err == nil && val > 0 {
					config.TruncateAfter = val
				}
				i++
			}
		case "--screenshot":
			if i+1 < len(args) {
				config.ScreenshotPath = args[i+1]
				i++
			}
		case "--form":
			if i+1 < len(args) {
				config.FormID = args[i+1]
				i++
			}
		case "--input":
			if i+1 < len(args) {
				name := args[i+1]
				i++
				if i+1 < len(args) && args[i+1] == "--value" {
					i++
					if i+1 < len(args) {
						value := args[i+1]
						config.Inputs = append(config.Inputs, FormInput{Name: name, Value: value})
						i++
					}
				}
			}
		case "--value":
			// Skip, handled with --input
		case "--after-submit":
			if i+1 < len(args) {
				config.AfterSubmitURL = ensureProtocol(args[i+1])
				i++
			}
		case "--js":
			if i+1 < len(args) {
				config.JSCode = args[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(args) {
				config.Profile = args[i+1]
				i++
			}
		case "--headful":
			config.Headful = true
		case "--window-size":
			if i+1 < len(args) {
				config.WindowSize = args[i+1]
				i++
			}
		default:
			if config.URL == "" && !strings.HasPrefix(arg, "--") {
				config.URL = arg
			}
		}
	}

	return config
}

func printHelp() {
	fmt.Printf(`surf - portable web scraper for llms

Usage: surf <url> [options]

Options:
  --help                     Show this help message
  --raw                      Output raw page instead of converting to markdown
  --truncate-after <number>  Truncate output after <number> characters and append a notice (default: %d)
  --screenshot <filepath>    Take a screenshot of the page and save it to the given filepath
  --form <id>                The id of the form for inputs
  --input <name>             Specify the name attribute for a form input field
  --value <value>            Provide the value to fill for the last --input field
  --after-submit <url>       After form submission and navigation, load this URL before converting to markdown
  --js <code>                Execute JavaScript code on the page after it loads
  --profile <name>           Use or create named session profile (default: "default")
  --headful                  Run browser in visible window mode (not headless)
  --window-size <WxH>        Set browser window size (e.g., 1280x720), useful with --headful

Phoenix LiveView Support:
This tool automatically detects Phoenix LiveView applications and properly handles:
- Connection waiting (.phx-connected)
- Form submissions with loading states
- State management between interactions

Examples:
  surf https://example.com
  surf https://example.com --screenshot page.png --truncate-after 5000
  surf https://example.com --headful --window-size 1920x1080
  surf localhost:4000/login --form login_form --input email --value test@example.com --input password --value secret
`, DEFAULT_TRUNCATE_AFTER)
}

// parseWindowSize parses a window size string like "1280x720" into width and height
func parseWindowSize(size string) (int, int) {
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 1280, 720 // default
	}
	width, err1 := strconv.Atoi(parts[0])
	height, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 1280, 720 // default
	}
	return width, height
}

// Ensure URL has protocol
func ensureProtocol(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "http://" + url
	}
	return url
}

// Clean markdown
func cleanMarkdown(markdown string) string {
	// Format headers properly
	markdown = strings.ReplaceAll(markdown, "\n# ", "\n# ")
	markdown = strings.ReplaceAll(markdown, "\n## ", "\n## ")
	markdown = strings.ReplaceAll(markdown, "\n### ", "\n### ")

	// Collapse multiple blank lines
	for strings.Contains(markdown, "\n\n\n") {
		markdown = strings.ReplaceAll(markdown, "\n\n\n", "\n\n")
	}

	// Normalize list bullets
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
			lines[i] = "- " + strings.TrimPrefix(strings.TrimPrefix(line, "* "), "- ")
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}
