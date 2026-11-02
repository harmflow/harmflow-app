package codex

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// tempDir is a temporary directory by its real path: on macOS the temporary directories are under /var, a symlink to
// /private/var.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// standInAt puts a stand-in whose --version and `login status` run the given shell commands in dir, under the CLI's
// name.
func standInAt(t *testing.T, dir, version, login string) string {
	t.Helper()
	data, err := os.ReadFile(standIn(t, version, login))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const signedIn = `echo "Logged in using ChatGPT" >&2`

// harmflow-app#68: detection searches PATH, then the known locations, and keeps every installation it finds in that
// order. The first is the one used, and its source says where it was found.
func TestDetectOnPathAndInKnownLocations(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	cases := []struct {
		name              string
		onPath, atKnown   bool
		wantSource        string
		wantVersion       string
		wantInstallations func(onPath, atKnown string) []string
	}{
		{"on PATH", true, false, "path", "0.163.0", func(p, _ string) []string { return []string{p} }},
		{"in a known location", false, true, "known_location", "0.150.0", func(_, k string) []string { return []string{k} }},
		{"in both", true, true, "path", "0.163.0", func(p, k string) []string { return []string{p, k} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			empty, pathDir, knownDir := tempDir(t), tempDir(t), tempDir(t)
			onPath, atKnown := filepath.Join(pathDir, "codex"), filepath.Join(knownDir, "codex")
			if c.onPath {
				standInAt(t, pathDir, `echo "codex-cli 0.163.0"`, signedIn)
			}
			if c.atKnown {
				standInAt(t, knownDir, `echo "codex-cli 0.150.0"`, signedIn)
			}
			a := &Adapter{Home: tempDir(t), PathEnv: empty + ":" + pathDir, KnownLocations: []string{atKnown}}
			info := a.Detect(context.Background(), "")
			want := c.wantInstallations(onPath, atKnown)
			used := want[0]
			if !info.Detected || info.Path != used || info.PathSource != c.wantSource || info.Version != c.wantVersion ||
				info.Readiness != "ready" || !slices.Equal(info.Installations, want) {
				t.Errorf("detected %v, path %q (%s), version %q, readiness %s, installations %q; want path %q (%s), "+
					"version %q, ready, installations %q", info.Detected, info.Path, info.PathSource, info.Version,
					info.Readiness, info.Installations, used, c.wantSource, c.wantVersion, want)
			}
		})
	}
}

// harmflow-app#68: each step of detection has its readiness, and the path detection found is absolute.
func TestDetectReadinessAtEachStep(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	version, signedOut := `echo "codex-cli 0.163.0"`, `echo "Not logged in" >&2; exit 1`
	cases := []struct {
		name, version, login   string
		installed              bool
		wantDetected           bool
		wantReadiness, wantKey string
	}{
		{"not found", "", "", false, false, "not_installed", "codex_not_found"},
		{"--version fails", `echo "error: cannot start" >&2; exit 1`, signedIn, true, true, "installed", "command_failed"},
		{"signed out", version, signedOut, true, true, "not_ready", "not_signed_in"},
		{"signed in", version, signedIn, true, true, "ready", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tempDir(t)
			if c.installed {
				standInAt(t, dir, c.version, c.login)
			}
			a := &Adapter{Home: tempDir(t), PathEnv: dir, KnownLocations: []string{}}
			info := a.Detect(context.Background(), "")
			if info.Detected != c.wantDetected || info.Readiness != c.wantReadiness || info.DiagnosticKey != c.wantKey {
				t.Errorf("detected %v, readiness %s, diagnostic %q (%s); want detected %v, readiness %s, diagnostic %q",
					info.Detected, info.Readiness, info.DiagnosticKey, info.Diagnostic, c.wantDetected, c.wantReadiness,
					c.wantKey)
			}
			if c.installed && (!filepath.IsAbs(info.Path) || info.Path != filepath.Join(dir, "codex")) {
				t.Errorf("path %q, want the absolute %q", info.Path, filepath.Join(dir, "codex"))
			}
			if !c.installed && (info.Path != "" || len(info.Installations) != 0) {
				t.Errorf("path %q, installations %q, for a harness that is not installed", info.Path, info.Installations)
			}
		})
	}
}

// harmflow-app#442: managed requirements that do not allow the approval policy or a sandbox mode HarmFlow runs Codex
// with leave it ready, with a diagnostic naming each setting and what they allow. Requirements that allow both, none at
// all, or that the app server does not give leave it as it would be with none.
func TestDetectReportsRequirementsThatOverrideASetting(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	requirements := func(approval, sandbox string) string {
		return answer(`{"requirements":{"allowedApprovalPolicies":` + approval + `,"allowedSandboxModes":` + sandbox + `}}`)
	}
	cases := []struct {
		name, onRequirements string
		wantKey              string
		wantParams           map[string]string
	}{
		// As Codex 0.163.0 answered on a machine whose requirements left out `never`.
		{"approval policy left out", requirements(`["on-request","untrusted"]`, `["read-only","workspace-write"]`),
			"codex_requirements_disallow",
			map[string]string{"settings": "approval_policy = never", "allowed": "approval_policy = on-request, untrusted"}},
		{"sandbox mode left out", requirements(`null`, `["read-only"]`), "codex_requirements_disallow",
			map[string]string{"settings": "sandbox_mode = workspace-write", "allowed": "sandbox_mode = read-only"}},
		{"both left out", requirements(`["untrusted"]`, `["danger-full-access"]`), "codex_requirements_disallow",
			map[string]string{"settings": "approval_policy = never; sandbox_mode = read-only, workspace-write",
				"allowed": "approval_policy = untrusted; sandbox_mode = danger-full-access"}},
		{"both allowed", requirements(`["never","on-request"]`, `["read-only","workspace-write","danger-full-access"]`),
			"", nil},
		{"no requirements", answer(`{"requirements":null}`), "", nil},
		{"an error", `printf '{"id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"`, "", nil},
		{"no answer", `:`, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, _ := appServerStandInWith(t, answer(`{"data":[]}`), c.onRequirements)
			a := appServerAdapter(t)
			a.listTimeout = time.Second
			info := a.Detect(context.Background(), path)
			if info.Readiness != "ready" || info.DiagnosticKey != c.wantKey || !maps.Equal(info.DiagnosticParams, c.wantParams) {
				t.Errorf("Detect(%s) readiness %s, diagnostic %q %v (%s); want ready, diagnostic %q %v", c.name,
					info.Readiness, info.DiagnosticKey, info.DiagnosticParams, info.Diagnostic, c.wantKey, c.wantParams)
			}
		})
	}
}

// harmflow-app#571: the check reads how Codex's account signs in and its plan, and whether an administrator manages
// its requirements, with what they allow. A plan Codex reports as unknown, or an account/read that fails, reads
// unknown with the reason and leaves Codex ready, its diagnostic untouched. No e-mail and no ID the app server
// answers with reaches what the check reports.
func TestDetectReadsTheAccountAndItsManagedRequirements(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	noRequirements := answer(`{"requirements":null}`)
	managed := answer(`{"requirements":{"allowedApprovalPolicies":["never","on-request"],` +
		`"allowedApprovalsReviewers":["user"],"allowedPermissionProfiles":["read-only","workspace-write"],` +
		`"allowedSandboxModes":null,"featureRequirements":{"web_search":false}}}`)
	account := func(fields string) string {
		return answer(`{"account":{` + fields + `,"email":"someone@example.com"},"requiresOpenaiAuth":true,` +
			`"workspaceRouting":{"accountId":"acct-7f3a9c","workspaceId":"ws-51d2e8"}}`)
	}
	cases := []struct {
		name, onRequirements, onAccount string
		want                            ports.HarnessAccount
		wantRequirements                ports.ManagedRequirements
	}{
		{"a personal plan", noRequirements, account(`"type":"chatgpt","planType":"plus"`),
			ports.HarnessAccount{SignInMethod: "chatgpt", Plan: "plus"}, ports.ManagedRequirements{}},
		{"a workspace plan with managed requirements", managed, account(`"type":"chatgpt","planType":"business"`),
			ports.HarnessAccount{SignInMethod: "chatgpt", Plan: "business"},
			ports.ManagedRequirements{Managed: true, ApprovalPolicies: []string{"never", "on-request"},
				ApprovalsReviewers: []string{"user"}, PermissionProfiles: []string{"read-only", "workspace-write"}}},
		{"an unknown plan", noRequirements, account(`"type":"chatgpt","planType":"unknown"`),
			ports.HarnessAccount{SignInMethod: "chatgpt", Plan: "unknown", PlanReasonKey: "codex_plan_unknown"},
			ports.ManagedRequirements{}},
		{"an API key", noRequirements, account(`"type":"apiKey"`), ports.HarnessAccount{SignInMethod: "api_key"},
			ports.ManagedRequirements{}},
		{"no account named", noRequirements, answer(`{"account":null,"requiresOpenaiAuth":true}`),
			ports.HarnessAccount{SignInMethod: "unknown", Plan: "unknown", PlanReasonKey: "codex_account_unnamed"},
			ports.ManagedRequirements{}},
		{"a failing account/read", noRequirements,
			`printf '{"id":%s,"error":{"code":-32603,"message":"account unavailable"}}\n' "$id"`,
			ports.HarnessAccount{SignInMethod: "unknown", Plan: "unknown", PlanReasonKey: "codex_app_server_error",
				PlanReasonParams: map[string]string{"method": "account/read", "detail": "account unavailable"}},
			ports.ManagedRequirements{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, received := appServerStandInAnswering(t, answer(`{"data":[]}`), c.onRequirements, c.onAccount)
			a := appServerAdapter(t)
			a.listTimeout = time.Second
			info := a.Detect(context.Background(), path)
			if info.Readiness != "ready" || info.DiagnosticKey != "" {
				t.Errorf("Detect(%s) readiness %s, diagnostic %q (%s); want ready, no diagnostic", c.name, info.Readiness,
					info.DiagnosticKey, info.Diagnostic)
			}
			got := info.Account
			if got == nil {
				t.Fatalf("Detect(%s).Account = nil, want %+v", c.name, c.want)
			}
			if got.SignInMethod != c.want.SignInMethod || got.Plan != c.want.Plan || got.PlanReasonKey != c.want.PlanReasonKey ||
				(c.want.PlanReasonParams != nil && !maps.Equal(got.PlanReasonParams, c.want.PlanReasonParams)) ||
				(got.PlanReasonKey != "" && got.PlanReason == "") {
				t.Errorf("Detect(%s).Account = %+v, want %+v", c.name, *got, c.want)
			}
			if r := info.ManagedRequirements; r == nil || !reflect.DeepEqual(*r, c.wantRequirements) {
				t.Errorf("Detect(%s).ManagedRequirements = %+v, want %+v", c.name, r, c.wantRequirements)
			}
			if p := requests(t, received)["account/read"]; len(p) != 1 || p[0] == nil || len(p[0]) != 0 {
				t.Errorf("Detect(%s) sent account/read with params %v, want once with {}", c.name, p)
			}
			data, err := json.Marshal(info)
			if err != nil {
				t.Fatalf("json.Marshal(Detect(%s)) = %v, want no error", c.name, err)
			}
			for _, secret := range []string{"someone@example.com", "acct-7f3a9c", "ws-51d2e8", "web_search"} {
				if strings.Contains(string(data), secret) {
					t.Errorf("Detect(%s) reports %s: %s", c.name, secret, data)
				}
			}
		})
	}
}

// harmflow-app#571: requirements that cannot be read leave whether they are managed unknown, and the account is read
// all the same.
func TestDetectWithRequirementsItCannotRead(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	path, _ := appServerStandInWith(t, answer(`{"data":[]}`),
		`printf '{"id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"`)
	a := appServerAdapter(t)
	a.listTimeout = time.Second
	info := a.Detect(context.Background(), path)
	if info.Readiness != "ready" || info.ManagedRequirements != nil || info.Account == nil || info.Account.Plan != "plus" {
		t.Errorf("Detect readiness %s, managed requirements %+v, account %+v; want ready, requirements unknown, the "+
			"plus plan", info.Readiness, info.ManagedRequirements, info.Account)
	}
}

// harmflow-app#68: what the shipped daemon searches stays the usual install locations; only the code that builds the
// adapter can replace them.
func TestKnownLocationsDefault(t *testing.T) {
	home := "/Users/someone"
	want := []string{
		"/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
		"/Users/someone/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
		"/Users/someone/.local/bin/codex",
		"/Users/someone/.npm-global/bin/codex",
		"/Users/someone/.bun/bin/codex",
	}
	if got := (&Adapter{Home: home}).knownLocations(); !slices.Equal(got, want) {
		t.Errorf("known locations %q, want %q", got, want)
	}
	if got := (&Adapter{Home: home, KnownLocations: []string{}}).knownLocations(); len(got) != 0 {
		t.Errorf("an empty list searches %q", got)
	}
}

// harmflow-app#70: a path set by hand is offered with the installations detection found, once, and it is the one in
// use.
func TestDetectOffersThePathSetByHand(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	pathDir, knownDir := tempDir(t), tempDir(t)
	onPath := standInAt(t, pathDir, `echo "codex-cli 0.163.0"`, signedIn)
	atKnown := standInAt(t, knownDir, `echo "codex-cli 0.150.0"`, signedIn)
	byHand := standInAt(t, tempDir(t), `echo "codex-cli 0.140.0"`, signedIn)
	gone := filepath.Join(tempDir(t), "codex")
	cases := []struct {
		name, manual                 string
		wantInstallation, wantSource string
		wantVersion, wantReadiness   string
		want                         []string
	}{
		{"none set", "", onPath, "path", "0.163.0", "ready", []string{onPath, atKnown}},
		{"one detection did not find", byHand, byHand, "manual", "0.140.0", "ready", []string{onPath, atKnown, byHand}},
		{"one detection found", atKnown, atKnown, "manual", "0.150.0", "ready", []string{onPath, atKnown}},
		{"a file that is gone", gone, gone, "manual", "", "not_installed", []string{onPath, atKnown, gone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: tempDir(t), PathEnv: pathDir, KnownLocations: []string{atKnown}}
			info := a.Detect(context.Background(), c.manual)
			if !slices.Equal(info.Installations, c.want) || info.Installation != c.wantInstallation ||
				info.Path != c.wantInstallation || info.PathSource != c.wantSource || info.Version != c.wantVersion ||
				info.Readiness != c.wantReadiness {
				t.Errorf("installations %q, installation %q, path %q (%s), version %q, readiness %s; want %q, %q (%s), %q, %s",
					info.Installations, info.Installation, info.Path, info.PathSource, info.Version, info.Readiness, c.want,
					c.wantInstallation, c.wantSource, c.wantVersion, c.wantReadiness)
			}
		})
	}
}
