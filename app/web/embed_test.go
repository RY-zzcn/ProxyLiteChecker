package webassets

import (
	"strings"
	"testing"
)

func TestEmbeddedConsoleIncludesV045LiveInterface(t *testing.T) {
	t.Parallel()
	checks := map[string][]string{
		"index.html": {
			`id="themeToggle"`,
			`class="workspace-nav"`,
			`<details class="settings-group`,
			`id="versionText">v0.4.5`,
			`id="runtimeLogs"`,
			`name="automation-settings"`,
		},
		"static/styles.css": {
			"v0.4.4 interface system",
			"v0.4.5 live operations refinement",
			`:root[data-theme="dark"]`,
			"@media (prefers-reduced-motion: reduce)",
		},
		"static/app.js": {
			`const THEME_STORAGE_KEY = "plc_theme"`,
			"setupSectionNavigation()",
			"loadRuntime()",
			`data-label="代理"`,
		},
	}
	for name, needles := range checks {
		raw, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		text := string(raw)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("embedded %s missing %q", name, needle)
			}
		}
	}
}
