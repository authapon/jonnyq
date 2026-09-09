package tools

import "regexp"

// dangerousPatterns catches the shell commands that can do irreversible
// system-wide damage (wipe a disk, nuke the filesystem root, halt the
// machine, pipe a remote script straight into a shell, etc). This is a
// best-effort denylist, not a sandbox: run_command still executes via
// `bash -c` with the caller's full privileges, so anything not matched here
// runs normally and no confirmation prompt is shown (by design).
var dangerousPatterns = []*regexp.Regexp{
	// rm -rf (in any flag order/spelling) targeting root-ish paths.
	regexp.MustCompile(`(?i)\brm\s+(-[a-z]*[rf][a-z]*[rf]?[a-z]*|--recursive)\s+(--no-preserve-root\s+)?(/|/\*|~|~/\*|\$HOME|\$HOME/\*)(\s|$)`),
	regexp.MustCompile(`(?i)--no-preserve-root`),
	// classic fork bomb.
	regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	// filesystem/partition destruction.
	regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b`),
	regexp.MustCompile(`(?i)\bfdisk\b`),
	regexp.MustCompile(`(?i)\bparted\b`),
	regexp.MustCompile(`(?i)\bwipefs\b`),
	regexp.MustCompile(`(?i)\bdd\s+.*\bof=/dev/`),
	regexp.MustCompile(`(?i)>\s*/dev/sd[a-z][0-9]*\b`),
	regexp.MustCompile(`(?i)>\s*/dev/nvme\d+n\d+`),
	// power/system state changes.
	regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff|halt)\b`),
	regexp.MustCompile(`(?i)\binit\s+[06]\b`),
	regexp.MustCompile(`(?i)\bsystemctl\s+(poweroff|reboot|halt)\b`),
	// account/auth tampering.
	regexp.MustCompile(`(?i)\b(useradd|userdel|usermod|passwd|visudo)\b`),
	regexp.MustCompile(`(?i)>\s*/etc/(passwd|shadow|sudoers)\b`),
	// wiping cron/firewall wholesale.
	regexp.MustCompile(`(?i)\bcrontab\s+-r\b`),
	regexp.MustCompile(`(?i)\biptables\s+(-F|--flush)\b`),
	// remote script piped straight into a shell.
	regexp.MustCompile(`(?i)\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(sh|bash|zsh)\b`),
	// recursive chmod/chown starting from root.
	regexp.MustCompile(`(?i)\bchmod\s+-R\s+\S+\s+/(\s|$)`),
	regexp.MustCompile(`(?i)\bchown\s+-R\s+\S+\s+/(\s|$)`),
}

// checkDenylist returns a non-empty reason if cmd matches a known-dangerous
// pattern.
func checkDenylist(cmd string) (reason string, blocked bool) {
	for _, re := range dangerousPatterns {
		if re.MatchString(cmd) {
			return "matches denylisted pattern: " + re.String(), true
		}
	}
	return "", false
}
