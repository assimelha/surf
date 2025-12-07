package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/jaytaylor/html2text"
)

const DEFAULT_TRUNCATE_AFTER = 100000

// Realistic Chrome user-agent for macOS
const STEALTH_USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Stealth JavaScript to mask automation indicators - runs before page scripts
const STEALTH_JS = `
(function() {
    // Strategy 1: Delete from prototype chain
    const proto = Object.getPrototypeOf(navigator);
    if ('webdriver' in proto) {
        delete proto.webdriver;
    }

    // Strategy 2: Override with a getter that always returns undefined
    // Use a closure to prevent any way to detect the original value
    const webdriverDescriptor = {
        get: function() { return undefined; },
        set: function(val) { /* ignore */ },
        configurable: false,
        enumerable: false
    };

    try {
        Object.defineProperty(navigator, 'webdriver', webdriverDescriptor);
    } catch(e) {
        // If it fails, try on prototype
        try {
            Object.defineProperty(proto, 'webdriver', webdriverDescriptor);
        } catch(e2) {}
    }

    // Strategy 3: Proxy the entire navigator object to intercept property access
    const navigatorProxy = new Proxy(navigator, {
        get: function(target, prop) {
            if (prop === 'webdriver') return undefined;
            const value = target[prop];
            return typeof value === 'function' ? value.bind(target) : value;
        },
        has: function(target, prop) {
            if (prop === 'webdriver') return false;
            return prop in target;
        }
    });

    // Replace navigator getter on window
    try {
        Object.defineProperty(window, 'navigator', {
            get: () => navigatorProxy,
            configurable: false
        });
    } catch(e) {
        // Fallback if window.navigator is not configurable
    }

    // Strategy 4: Intercept property descriptor access
    const origGetOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
    Object.getOwnPropertyDescriptor = function(obj, prop) {
        if ((obj === navigator || obj === navigatorProxy) && prop === 'webdriver') {
            return undefined;
        }
        return origGetOwnPropertyDescriptor.apply(this, arguments);
    };

    const origGetOwnPropertyDescriptors = Object.getOwnPropertyDescriptors;
    Object.getOwnPropertyDescriptors = function(obj) {
        const result = origGetOwnPropertyDescriptors.apply(this, arguments);
        if (obj === navigator || obj === navigatorProxy) {
            delete result.webdriver;
        }
        return result;
    };

    // Strategy 5: Override hasOwnProperty
    const origHasOwnProperty = Object.prototype.hasOwnProperty;
    Object.prototype.hasOwnProperty = function(prop) {
        if ((this === navigator || this === navigatorProxy) && prop === 'webdriver') {
            return false;
        }
        return origHasOwnProperty.apply(this, arguments);
    };

    // Strategy 6: Handle 'in' operator via prototype manipulation
    const origPropertyIsEnumerable = Object.prototype.propertyIsEnumerable;
    Object.prototype.propertyIsEnumerable = function(prop) {
        if ((this === navigator || this === navigatorProxy) && prop === 'webdriver') {
            return false;
        }
        return origPropertyIsEnumerable.apply(this, arguments);
    };
})();

// Override navigator.plugins to look like real browser with proper PluginArray prototype
(function() {
    const makePluginArray = () => {
        const plugins = [
            { name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer', description: 'Portable Document Format', length: 1 },
            { name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai', description: '', length: 1 },
            { name: 'Native Client', filename: 'internal-nacl-plugin', description: '', length: 1 }
        ];
        // Add MimeType-like objects to each plugin
        plugins.forEach((p, i) => {
            p[0] = { type: 'application/pdf', suffixes: 'pdf', description: p.description, enabledPlugin: p };
        });
        plugins.item = function(i) { return this[i] || null; };
        plugins.namedItem = function(name) { return this.find(p => p.name === name) || null; };
        plugins.refresh = function() {};
        // Make it inherit from PluginArray prototype
        Object.setPrototypeOf(plugins, PluginArray.prototype);
        return plugins;
    };
    const pluginArray = makePluginArray();
    Object.defineProperty(navigator, 'plugins', {
        get: () => pluginArray,
        enumerable: true,
        configurable: false
    });
})();

// Override navigator.languages
Object.defineProperty(navigator, 'languages', {
    get: () => ['en-US', 'en'],
    configurable: true
});

// Override navigator.permissions.query
const originalQuery = window.navigator.permissions.query;
window.navigator.permissions.query = (parameters) => (
    parameters.name === 'notifications' ?
        Promise.resolve({ state: Notification.permission }) :
        originalQuery(parameters)
);

// Add chrome runtime
window.chrome = {
    runtime: {},
    loadTimes: function() {},
    csi: function() {},
    app: {}
};

// Override iframe contentWindow access pattern detection
const originalAttachShadow = Element.prototype.attachShadow;
Element.prototype.attachShadow = function(init) {
    if (init && init.mode === 'closed') {
        init.mode = 'open';
    }
    return originalAttachShadow.call(this, init);
};

// Mask automation in WebGL
const getParameterProxyHandler = {
    apply: function(target, thisArg, args) {
        const param = args[0];
        const gl = thisArg;
        // UNMASKED_VENDOR_WEBGL
        if (param === 37445) {
            return 'Intel Inc.';
        }
        // UNMASKED_RENDERER_WEBGL
        if (param === 37446) {
            return 'Intel Iris OpenGL Engine';
        }
        return Reflect.apply(target, thisArg, args);
    }
};

try {
    const canvas = document.createElement('canvas');
    const gl = canvas.getContext('webgl') || canvas.getContext('experimental-webgl');
    if (gl) {
        WebGLRenderingContext.prototype.getParameter = new Proxy(
            WebGLRenderingContext.prototype.getParameter,
            getParameterProxyHandler
        );
    }
    const gl2 = canvas.getContext('webgl2');
    if (gl2) {
        WebGL2RenderingContext.prototype.getParameter = new Proxy(
            WebGL2RenderingContext.prototype.getParameter,
            getParameterProxyHandler
        );
    }
} catch(e) {}

console.log('[stealth] Anti-detection measures applied');
`

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
	Session        string
	StopSession    bool
	Stealth        bool
}

type SessionInfo struct {
	WSURL    string `json:"ws_url"`
	Profile  string `json:"profile"`
	Headful  bool   `json:"headful"`
	PID      int    `json:"pid"`
	TargetID string `json:"target_id"`
}

func main() {
	config := parseArgs()

	// Handle --stop flag for session
	if config.StopSession {
		if config.Session == "" {
			fmt.Fprintf(os.Stderr, "Error: --stop requires --session <id>\n")
			os.Exit(1)
		}
		if err := stopSession(config.Session); err != nil {
			fmt.Fprintf(os.Stderr, "Error stopping session: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Session '%s' stopped\n", config.Session)
		return
	}

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

func stopSession(sessionID string) error {
	info, err := loadSession(sessionID)
	if err != nil {
		return fmt.Errorf("session '%s' not found", sessionID)
	}

	// Kill the browser process
	if info.PID > 0 {
		proc, err := os.FindProcess(info.PID)
		if err == nil {
			proc.Signal(os.Interrupt)
			time.Sleep(500 * time.Millisecond)
			proc.Kill()
		}
	}

	// Remove session file
	return removeSession(sessionID)
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

func getSessionsDir() string {
	return filepath.Join(getChromiumDir(), "sessions")
}

func getSessionFile(sessionID string) string {
	return filepath.Join(getSessionsDir(), sessionID+".json")
}

func saveSession(sessionID string, info SessionInfo) error {
	sessionsDir := getSessionsDir()
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(getSessionFile(sessionID), data, 0644)
}

func loadSession(sessionID string) (*SessionInfo, error) {
	data, err := os.ReadFile(getSessionFile(sessionID))
	if err != nil {
		return nil, err
	}
	var info SessionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func removeSession(sessionID string) error {
	return os.Remove(getSessionFile(sessionID))
}

// startSessionBrowser starts a Chrome process for a persistent session
func startSessionBrowser(config Config) (*SessionInfo, error) {
	chromiumExec := getChromiumExec()
	chromiumDir := getChromiumDir()
	profileDir := filepath.Join(chromiumDir, "profiles", config.Profile)
	os.MkdirAll(profileDir, 0755)

	// Find a free port for remote debugging
	port := findFreePort()

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-default-apps",
		"--no-first-run",
		"--disable-fre",
	}

	// Add stealth flags if enabled
	if config.Stealth {
		args = append(args,
			"--disable-blink-features=AutomationControlled",
			fmt.Sprintf("--user-agent=%s", STEALTH_USER_AGENT),
		)
	}

	if !config.Headful {
		args = append(args, "--headless=new")
	}

	if config.WindowSize != "" {
		w, h := parseWindowSize(config.WindowSize)
		args = append(args, fmt.Sprintf("--window-size=%d,%d", w, h))
	}

	// Start with a blank page
	args = append(args, "about:blank")

	cmd := exec.Command(chromiumExec, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach process so it survives after surf exits
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start browser: %v", err)
	}

	// Wait for browser to be ready and get websocket URL
	wsURL, err := waitForBrowserReady(port, 10*time.Second)
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("browser failed to start: %v", err)
	}

	return &SessionInfo{
		WSURL:   wsURL,
		Profile: config.Profile,
		Headful: config.Headful,
		PID:     cmd.Process.Pid,
	}, nil
}

func findFreePort() int {
	// Start from a high port and find one that works
	// The browser will bind to this port for debugging
	return 9222 + int(time.Now().UnixNano()%1000)
}

func waitForBrowserReady(port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)

	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
				if wsURL, ok := result["webSocketDebuggerUrl"].(string); ok {
					return wsURL, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("timeout waiting for browser on port %d", port)
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

	var ctx context.Context
	var cancel context.CancelFunc
	var allocCancel context.CancelFunc
	isSession := config.Session != ""
	var sessionInfo *SessionInfo

	if isSession {
		// Session mode: connect to existing or start new browser
		existingSession, err := loadSession(config.Session)
		if err == nil {
			// Connect to existing session
			sessionInfo = existingSession
			fmt.Fprintf(os.Stderr, "Connecting to session '%s'...\n", config.Session)
		} else {
			// Start new session browser
			fmt.Fprintf(os.Stderr, "Starting new session '%s'...\n", config.Session)
			sessionInfo, err = startSessionBrowser(config)
			if err != nil {
				return "", err
			}
		}

		// Connect to the browser via websocket
		allocCtx, allocCancelFunc := chromedp.NewRemoteAllocator(context.Background(), sessionInfo.WSURL)
		allocCancel = allocCancelFunc

		if sessionInfo.TargetID != "" {
			// Attach to existing tab
			ctx, cancel = chromedp.NewContext(allocCtx,
				chromedp.WithTargetID(target.ID(sessionInfo.TargetID)))
		} else {
			// Create new tab
			ctx, cancel = chromedp.NewContext(allocCtx)
		}

		// If this is a new session (no target ID yet), save it after first run
		if sessionInfo.TargetID == "" {
			// We need to run something to create the target, then get its ID
			if err := chromedp.Run(ctx); err != nil {
				return "", fmt.Errorf("failed to initialize browser context: %v", err)
			}
			sessionInfo.TargetID = string(chromedp.FromContext(ctx).Target.TargetID)
			if err := saveSession(config.Session, *sessionInfo); err != nil {
				return "", fmt.Errorf("failed to save session: %v", err)
			}
		}
	} else {
		// One-shot mode: start fresh browser that will be closed
		chromiumExec := getChromiumExec()
		chromiumDir := getChromiumDir()
		profileDir := filepath.Join(chromiumDir, "profiles", config.Profile)
		os.MkdirAll(profileDir, 0755)

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

		// Add stealth options if enabled
		if config.Stealth {
			opts = append(opts,
				chromedp.Flag("disable-blink-features", "AutomationControlled"),
				chromedp.UserAgent(STEALTH_USER_AGENT),
			)
		}

		if config.WindowSize != "" {
			opts = append(opts, chromedp.WindowSize(parseWindowSize(config.WindowSize)))
		}

		allocCtx, allocCancelFunc := chromedp.NewExecAllocator(context.Background(), opts...)
		allocCancel = allocCancelFunc

		ctx, cancel = chromedp.NewContext(allocCtx)
	}

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

	// Inject stealth JS before navigation if enabled (runs before any page scripts)
	if config.Stealth {
		err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(STEALTH_JS).Do(ctx)
			return err
		}))
		if err != nil {
			// Non-fatal, log and continue
			fmt.Fprintf(os.Stderr, "Warning: Could not inject stealth script: %v\n", err)
		}
	}

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

	if isSession {
		// Session mode: keep browser and tab running
		// Don't call cancel() as it may close the tab
		// Just let the context go out of scope
		_ = cancel
		_ = allocCancel
	} else {
		// One-shot mode: close browser
		// Navigate away to trigger localStorage flush before shutdown
		chromedp.Run(ctx, chromedp.Navigate("about:blank"))
		time.Sleep(100 * time.Millisecond)

		// Explicitly cancel context to ensure browser shuts down
		timeoutCancel()
		cancel()
		// Wait for browser process to fully exit and flush data
		time.Sleep(500 * time.Millisecond)
		allocCancel()
	}

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
		case "--quickstart":
			printQuickstart()
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
		case "--session":
			if i+1 < len(args) {
				config.Session = args[i+1]
				i++
			}
		case "--stop":
			config.StopSession = true
		case "--stealth":
			config.Stealth = true
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
  --quickstart               Show detailed usage guide for AI agents
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
  --session <id>             Use persistent browser session (stays open between calls)
  --stop                     Stop a persistent session (requires --session)
  --stealth                  Enable anti-detection mode (realistic user-agent, hide automation)

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

func printQuickstart() {
	fmt.Print(`surf - Web Browser for LLMs

Converts web pages to markdown with full JavaScript execution.
Fork of chrismccord/web.

BASIC USAGE
  surf https://example.com          Convert page to markdown
  surf example.com                  Protocol auto-added (http://)

OUTPUT MODES
  surf https://example.com          Markdown output (default, optimized for LLMs)
  surf https://example.com --raw    Raw HTML output
  surf url --truncate-after 5000    Limit output to 5000 chars

SCREENSHOTS
  surf https://example.com --screenshot page.png
  surf https://example.com --screenshot shot.png --truncate-after 5000

JAVASCRIPT EXECUTION
  surf https://example.com --js "document.querySelector('button').click()"
  surf https://example.com --js "console.log(document.title)"
  Console output (log/warn/error) is captured and appended to output.

FORM FILLING
  surf https://login.example.com \
      --form "login_form" \
      --input "username" --value "myuser" \
      --input "password" --value "mypass"

  Navigate after submit:
  surf https://login.example.com \
      --form "login_form" \
      --input "email" --value "me@example.com" \
      --after-submit "https://example.com/dashboard"

HEADFUL MODE (visible browser for debugging)
  surf https://example.com --headful
  surf https://example.com --headful --window-size 1920x1080

PERSISTENT SESSIONS (keep browser open between calls)
  surf https://example.com --session myapp            # Start session, fetch page
  surf https://example.com/page2 --session myapp      # Reuse same browser
  surf https://example.com/page3 --session myapp --screenshot page.png
  surf --session myapp --stop                         # Close browser when done

  Multiple sessions can run in parallel:
  surf https://site-a.com --session agent1 --headful
  surf https://site-b.com --session agent2 --headful

SESSION PROFILES (persistent cookies/auth)
  surf --profile "github" https://github.com
  surf --profile "github" https://github.com/settings
  Profiles stored in ~/.surf/profiles/<name>/

PHOENIX LIVEVIEW
  Automatically detected and handled:
  - Waits for .phx-connected before proceeding
  - Handles LiveView form submissions correctly
  - Manages navigation events and state updates

STEALTH MODE (anti-detection)
  surf https://example.com --stealth
  Enables anti-detection measures:
  - Realistic Chrome user-agent (not "Chrome for Testing")
  - Hides navigator.webdriver property
  - Disables automation-controlled blink features
  - Spoofs plugins, languages, and WebGL fingerprints

AGENT INTEGRATION TIPS
  - Output is markdown, optimized for LLM context windows
  - Console logs captured and appended (useful for debugging)
  - Use --truncate-after to limit output size for large pages
  - Use --screenshot to verify visual state
  - Profiles persist auth across multiple surf calls
  - Combine --js with --screenshot to capture post-interaction state
  - Use --session for multi-step workflows (faster, maintains state)
  - Multiple agents can use separate --session IDs in parallel
  - Use --stealth when scraping sites with bot detection

EXAMPLES
  # Scrape and summarize
  surf https://news.ycombinator.com

  # Login and capture authenticated page
  surf https://app.example.com/login \
      --form "login" --input "email" --value "me@x.com" \
      --input "password" --value "secret" \
      --after-submit "https://app.example.com/dashboard" \
      --profile "myapp"

  # Click a button and screenshot result
  surf https://example.com \
      --js "document.querySelector('#submit-btn').click()" \
      --screenshot result.png

  # Debug with visible browser
  surf https://example.com --headful --window-size 1280x720
`)
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
