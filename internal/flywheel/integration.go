package flywheel

// IntegrationBranch returns the branch units integrate into as dir sees it
// (issue #456): integration.branch when the config sets it (configured true,
// whether or not refs/heads/<branch> resolves), else main when refs/heads/main
// exists, else master, else "". A config that fails to load counts as unset;
// the commands that load it report the error themselves.
func IntegrationBranch(dir string) (branch string, configured bool) {
	if c, _, err := LoadConfig(dir); err == nil && c.Integration != nil && c.Integration.Branch != "" {
		return c.Integration.Branch, true
	}
	for _, b := range []string{"main", "master"} {
		if branchResolves(dir, b) {
			return b, false
		}
	}
	return "", false
}

// integrationOrMain is IntegrationBranch(dir), "main" when that is "": the
// default base of the commands that assumed main before issue #456.
func integrationOrMain(dir string) string {
	if b, _ := IntegrationBranch(dir); b != "" {
		return b
	}
	return "main"
}

// branchResolves reports whether refs/heads/<b> resolves in dir.
func branchResolves(dir, b string) bool {
	rc, _, _, err := runCmdSplit(dir, gitArgs([]string{"rev-parse", "--verify", "-q", "refs/heads/" + b}), nil)
	return err == nil && rc == 0
}
