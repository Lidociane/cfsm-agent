package cfprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	autoUpdateCheckInterval = 6 * time.Hour
	autoUpdateLockTTL       = 30 * time.Minute
	defaultUpdateRepo       = "huilang-me/cfsm-agent"
	githubAPIBaseURL        = "https://api.github.com"
	snapshotVersionPrefix   = "Snapshot-"
)

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt time.Time            `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type updateCandidate struct {
	TagName     string
	AssetName   string
	Snapshot    bool
	PublishedAt time.Time
}

type updateVersion struct {
	major int
	minor int
	patch int
	pre   []string
}

func (a *Agent) autoUpdateWorker(ctx context.Context) {
	a.checkAndScheduleAgentUpdate("startup")

	ticker := time.NewTicker(autoUpdateCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkAndScheduleAgentUpdate("periodic")
		}
	}
}

func (a *Agent) checkAndScheduleAgentUpdate(reason string) {
	if !a.cfg.AutoUpdate {
		a.log.warnf("auto update ignored: local AUTO_UPDATE=0")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	candidate, ok, err := checkLatestUpdate(ctx, a.version, a.cfg.UpdateProxy)
	if err != nil {
		a.log.warnf("auto update check failed: %v", err)
		return
	}
	if !ok {
		a.log.info("auto update checked: current version is up to date reason=%s version=%s", reason, a.version)
		return
	}

	a.scheduleAgentUpdate(candidate, reason)
}

func checkLatestUpdate(ctx context.Context, currentVersion, proxy string) (updateCandidate, bool, error) {
	owner, name, err := splitUpdateRepoSlug(defaultUpdateRepo)
	if err != nil {
		return updateCandidate{}, false, err
	}
	assetName := expectedUpdateAssetName(runtime.GOOS, runtime.GOARCH)
	releases, err := listGitHubReleases(ctx, owner, name, proxy)
	if err != nil {
		return updateCandidate{}, false, err
	}

	if strings.HasPrefix(currentVersion, snapshotVersionPrefix) {
		candidate, ok := selectLatestSnapshotRelease(releases, assetName)
		if !ok || candidate.TagName == currentVersion {
			return updateCandidate{}, false, nil
		}
		return candidate, true, nil
	}

	current, err := parseUpdateVersion(currentVersion)
	if err != nil {
		return updateCandidate{}, false, fmt.Errorf("parse current version %q: %w", currentVersion, err)
	}
	candidate, ok := selectLatestStableRelease(releases, assetName, current)
	if !ok {
		return updateCandidate{}, false, nil
	}
	return candidate, true, nil
}

func listGitHubReleases(ctx context.Context, owner, repo, proxy string) ([]githubRelease, error) {
	client := http.Client{Timeout: 30 * time.Second}
	var releases []githubRelease
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d",
			githubAPIBaseURL, url.PathEscape(owner), url.PathEscape(repo), page)
		endpoint = applyURLProxy(endpoint, proxy)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "cfsm-agent")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("GitHub releases API returned http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var pageReleases []githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&pageReleases); err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		_ = resp.Body.Close()
		releases = append(releases, pageReleases...)
		if len(pageReleases) < 100 {
			return releases, nil
		}
	}
}

func selectLatestStableRelease(releases []githubRelease, assetName string, current updateVersion) (updateCandidate, bool) {
	var latest updateCandidate
	var latestVersion updateVersion
	found := false
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		if _, ok := findReleaseAsset(release, assetName); !ok {
			continue
		}
		version, err := parseUpdateVersion(release.TagName)
		if err != nil || compareUpdateVersion(version, current) <= 0 {
			continue
		}
		if !found || compareUpdateVersion(version, latestVersion) > 0 ||
			(compareUpdateVersion(version, latestVersion) == 0 && release.PublishedAt.After(latest.PublishedAt)) {
			latest = updateCandidate{
				TagName:     release.TagName,
				AssetName:   assetName,
				PublishedAt: release.PublishedAt,
			}
			latestVersion = version
			found = true
		}
	}
	return latest, found
}

func selectLatestSnapshotRelease(releases []githubRelease, assetName string) (updateCandidate, bool) {
	var latest updateCandidate
	found := false
	for _, release := range releases {
		if release.Draft || !release.Prerelease || !strings.HasPrefix(release.TagName, snapshotVersionPrefix) {
			continue
		}
		if _, ok := findReleaseAsset(release, assetName); !ok {
			continue
		}
		if !found || release.PublishedAt.After(latest.PublishedAt) ||
			(release.PublishedAt.Equal(latest.PublishedAt) && release.TagName > latest.TagName) {
			latest = updateCandidate{
				TagName:     release.TagName,
				AssetName:   assetName,
				Snapshot:    true,
				PublishedAt: release.PublishedAt,
			}
			found = true
		}
	}
	return latest, found
}

func findReleaseAsset(release githubRelease, assetName string) (githubReleaseAsset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func expectedUpdateAssetName(goos, goarch string) string {
	name := fmt.Sprintf("cf-probe-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func (a *Agent) scheduleAgentUpdate(candidate updateCandidate, reason string) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()

	lockFile := filepath.Join(a.paths.ConfigDir, "auto_update.lock")
	now := time.Now().Unix()
	if data, err := os.ReadFile(lockFile); err == nil {
		last := atoi64Default(string(data), 0)
		if time.Duration(now-last)*time.Second < autoUpdateLockTTL {
			a.log.warnf("auto update already scheduled recently")
			return
		}
	}

	scriptURL, err := updateInstallScriptURL(runtime.GOOS, a.cfg.UpdateProxy)
	if err != nil {
		a.log.warnf("auto update skipped: %v", err)
		return
	}

	method, err := scheduleInstallScript(a.paths.ServiceName, scriptURL, candidate.TagName, a.cfg.UpdateProxy, now)
	if err != nil {
		a.log.warnf("schedule update failed: %v", err)
		return
	}
	_ = os.MkdirAll(a.paths.ConfigDir, 0o755)
	_ = os.WriteFile(lockFile, []byte(strconv.FormatInt(now, 10)), 0o600)
	a.log.info("auto update scheduled target=%s asset=%s method=%s reason=%s delay=%s",
		candidate.TagName, candidate.AssetName, method, reason, autoUpdateDelay)
}

func scheduleInstallScript(serviceName, scriptURL, tag, proxy string, now int64) (string, error) {
	if runtime.GOOS == "windows" {
		return scheduleWindowsInstallScript(scriptURL, tag, proxy)
	}
	return scheduleUnixInstallScript(serviceName, scriptURL, tag, proxy, now)
}

func scheduleUnixInstallScript(serviceName, scriptURL, tag, proxy string, now int64) (string, error) {
	args := []string{"install", "--install-version=" + tag}
	if proxy != "" {
		args = append(args, "--install-ghproxy="+proxy)
	}
	cmdLine := fmt.Sprintf("sleep %d; curl -fsSL --connect-timeout 5 -m 30 %s | sh -s -- %s",
		int(autoUpdateDelay.Seconds()), quoteShell(scriptURL), quoteShellArgs(args))
	if runtime.GOOS == "linux" && fileExists("/run/systemd/system") {
		unit := fmt.Sprintf("%s-auto-update-%d", serviceName, now)
		if commandExists("systemd-run") {
			out, err := exec.Command("systemd-run", "--unit="+unit, "/bin/sh", "-c", cmdLine).CombinedOutput()
			if err == nil {
				return "systemd-run:" + unit, nil
			}
			if !commandExists("systemctl") {
				return "", fmt.Errorf("systemd-run failed: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		return scheduleSystemdUnit(unit, cmdLine)
	}

	cmd := exec.Command("sh", "-c", "nohup /bin/sh -c "+quoteShell(cmdLine)+" >/dev/null 2>&1 &")
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return "nohup", nil
}

func scheduleWindowsInstallScript(scriptURL, tag, proxy string) (string, error) {
	args := []string{"install", "--install-version=" + tag}
	if proxy != "" {
		args = append(args, "--install-ghproxy="+proxy)
	}
	script := strings.Join([]string{
		fmt.Sprintf("Start-Sleep -Seconds %d", int(autoUpdateDelay.Seconds())),
		"$script = Join-Path $env:TEMP 'install-cf-probe.ps1'",
		"Invoke-WebRequest -Uri " + powerShellLiteral(scriptURL) + " -OutFile $script -UseBasicParsing",
		"& PowerShell -NoProfile -ExecutionPolicy Bypass -File $script " + powerShellArgs(args),
	}, "; ")
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return "powershell", nil
}

func updateInstallScriptURL(goos, proxy string) (string, error) {
	owner, repo, err := splitUpdateRepoSlug(defaultUpdateRepo)
	if err != nil {
		return "", err
	}
	name := "install.sh"
	if goos == "windows" {
		name = "install.ps1"
	}
	raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s",
		url.PathEscape(owner), url.PathEscape(repo), name)
	return applyURLProxy(raw, proxy), nil
}

func splitUpdateRepoSlug(slug string) (string, string, error) {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo slug %q, expected owner/name", slug)
	}
	return parts[0], parts[1], nil
}

func powerShellLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteShell(arg))
	}
	return strings.Join(quoted, " ")
}

func powerShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, powerShellLiteral(arg))
	}
	return strings.Join(quoted, " ")
}

func applyURLProxy(raw, proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return raw
	}
	return strings.TrimRight(proxy, "/") + "/" + raw
}

func scheduleSystemdUnit(unit, cmdLine string) (string, error) {
	if !commandExists("systemctl") {
		return "", errors.New("systemctl unavailable under systemd")
	}
	serviceFile := filepath.Join("/run/systemd/system", unit+".service")
	content := fmt.Sprintf(`[Unit]
Description=CF Probe auto update

[Service]
Type=oneshot
ExecStart=/bin/sh -c %s

[Install]
WantedBy=multi-user.target
`, quoteSystemdExecArg(cmdLine))
	if err := os.WriteFile(serviceFile, []byte(content), 0o644); err != nil {
		return "", err
	}
	_ = runCommandQuiet("systemctl", "daemon-reload")
	out, err := exec.Command("systemctl", "start", unit+".service").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("systemctl start failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return "systemd-unit:" + unit, nil
}

func quoteSystemdExecArg(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`%`, `%%`,
		"\n", " ",
	)
	return `"` + replacer.Replace(s) + `"`
}

func parseUpdateVersion(raw string) (updateVersion, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "v")
	raw = strings.TrimPrefix(raw, "V")
	if raw == "" {
		return updateVersion{}, errors.New("empty version")
	}
	if i := strings.Index(raw, "+"); i >= 0 {
		raw = raw[:i]
	}
	core := raw
	var pre []string
	if i := strings.Index(core, "-"); i >= 0 {
		preRaw := core[i+1:]
		core = core[:i]
		if preRaw == "" {
			return updateVersion{}, errors.New("empty prerelease")
		}
		pre = strings.Split(preRaw, ".")
		for _, part := range pre {
			if part == "" {
				return updateVersion{}, errors.New("empty prerelease identifier")
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return updateVersion{}, fmt.Errorf("invalid semver core %q", core)
	}
	nums := [3]int{}
	for i, part := range parts {
		if part == "" {
			return updateVersion{}, fmt.Errorf("empty semver component in %q", core)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return updateVersion{}, fmt.Errorf("invalid semver component %q", part)
		}
		nums[i] = n
	}
	return updateVersion{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, nil
}

func compareUpdateVersion(a, b updateVersion) int {
	switch {
	case a.major != b.major:
		return compareInt(a.major, b.major)
	case a.minor != b.minor:
		return compareInt(a.minor, b.minor)
	case a.patch != b.patch:
		return compareInt(a.patch, b.patch)
	default:
		return comparePrerelease(a.pre, b.pre)
	}
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		aNum, aOK := numericIdentifier(a[i])
		bNum, bOK := numericIdentifier(b[i])
		switch {
		case aOK && bOK && aNum != bNum:
			return compareInt(aNum, bNum)
		case aOK && !bOK:
			return -1
		case !aOK && bOK:
			return 1
		case !aOK && !bOK && a[i] != b[i]:
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return compareInt(len(a), len(b))
}

func numericIdentifier(raw string) (int, bool) {
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
