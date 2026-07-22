package setup

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed plist.tmpl
var plistTemplateStr string

//go:embed app_info.plist
var appInfoPlist []byte

const (
	// BinaryName is the name of the installed binary.
	BinaryName = "reminderrelay"

	// PlistLabel is the launchd job label.
	PlistLabel = "com.github.njoerd114.reminderrelay"
)

// plistData holds template values for the launchd plist.
type plistData struct {
	BinaryPath string
	HomeDir    string
}

// AppInstallPath returns the per-user application bundle path.
func AppInstallPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, "Applications", "ReminderRelay.app")
}

// BinaryInstallPath returns the executable inside the application bundle.
func BinaryInstallPath() string {
	appPath := AppInstallPath()
	if appPath == "" {
		return ""
	}
	return filepath.Join(appPath, "Contents", "MacOS", BinaryName)
}

// PlistPath returns the launchd plist destination path.
func PlistPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents", PlistLabel+".plist")
}

// LogDir returns the log directory path.
func LogDir(homeDir string) string {
	return filepath.Join(homeDir, "Library", "Logs", BinaryName)
}

// InstallBinary assembles and signs the per-user application bundle. A real
// bundle identity is required for a background process to own a macOS TCC
// Reminders grant; a bare CLI can only inherit its launching terminal's grant.
func InstallBinary() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving current executable path: %w", err)
	}

	// Resolve symlinks so we copy the actual binary.
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolving executable symlinks: %w", err)
	}

	dest := BinaryInstallPath()
	if dest == "" || !filepath.IsAbs(dest) {
		return fmt.Errorf("resolving application install path")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating application bundle: %w", err)
	}
	if err := os.WriteFile(filepath.Join(AppInstallPath(), "Contents", "Info.plist"), appInfoPlist, 0o644); err != nil {
		return fmt.Errorf("writing application Info.plist: %w", err)
	}
	if err := copyFile(self, dest, 0o755); err != nil {
		return err
	}

	identity := os.Getenv("REMINDERRELAY_CODESIGN_IDENTITY")
	if identity == "" {
		identity = "-"
	}
	//nolint:gosec // arguments are passed directly, without a shell
	sign := exec.Command("codesign", "--force", "--deep", "--options", "runtime", "--timestamp=none", "--sign", identity, AppInstallPath())
	if output, err := sign.CombinedOutput(); err != nil {
		return fmt.Errorf("signing application bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}

	register := exec.Command(
		"/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
		"-f", AppInstallPath(),
	)
	if output, err := register.CombinedOutput(); err != nil {
		return fmt.Errorf("registering application bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// WritePlist renders the launchd plist from the embedded template and writes
// it to ~/Library/LaunchAgents/.
func WritePlist(homeDir string) error {
	tmpl, err := template.New("plist").Parse(plistTemplateStr)
	if err != nil {
		return fmt.Errorf("parsing plist template: %w", err)
	}

	data := plistData{
		BinaryPath: BinaryInstallPath(),
		HomeDir:    homeDir,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("executing plist template: %w", err)
	}

	dest := PlistPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing plist to %s: %w", dest, err)
	}
	return nil
}

// CreateLogDir creates the ~/Library/Logs/reminderrelay/ directory.
func CreateLogDir(homeDir string) error {
	dir := LogDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating log directory %s: %w", dir, err)
	}
	return nil
}

// LoadDaemon loads the launchd plist so the daemon starts immediately.
// If already loaded, it is unloaded first.
func LoadDaemon(homeDir string) error {
	plist := PlistPath(homeDir)
	_ = UnloadDaemon(homeDir) // ignore error if not loaded
	//nolint:gosec // user-controlled path
	cmd := exec.Command("launchctl", "load", plist)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// UnloadDaemon unloads the launchd plist (stops the daemon).
func UnloadDaemon(homeDir string) error {
	plist := PlistPath(homeDir)
	if _, err := os.Stat(plist); os.IsNotExist(err) {
		return nil // nothing to unload
	}
	//nolint:gosec // user-controlled path
	cmd := exec.Command("launchctl", "unload", plist)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl unload: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// RemovePlist deletes the launchd plist file.
func RemovePlist(homeDir string) error {
	plist := PlistPath(homeDir)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist %s: %w", plist, err)
	}
	return nil
}

// RemoveBinary deletes the per-user application bundle.
func RemoveBinary() error {
	appPath := AppInstallPath()
	if appPath == "" || !filepath.IsAbs(appPath) {
		return fmt.Errorf("resolving application install path")
	}
	return os.RemoveAll(appPath)
}

// IsDaemonLoaded checks whether the launchd job is currently loaded.
func IsDaemonLoaded() bool {
	cmd := exec.Command("launchctl", "list", PlistLabel)
	return cmd.Run() == nil
}

// PurgeUserData removes config, state database, and log files.
func PurgeUserData(homeDir string) error {
	dirs := []string{
		filepath.Join(homeDir, ".config", BinaryName),
		filepath.Join(homeDir, ".local", "share", BinaryName),
		LogDir(homeDir),
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing %s: %w", dir, err)
		}
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

// copyFile copies src to dst with the given permissions.
func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
