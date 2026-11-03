package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Release builds set these with -ldflags -X. Ordinary go build/go install
// commands can still report the module version and VCS revision below.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func versionString() string {
	info, _ := debug.ReadBuildInfo()
	return formatVersion(version, commit, date, info)
}

func formatVersion(version, commit, date string, info *debug.BuildInfo) string {
	if info != nil {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = strings.TrimPrefix(info.Main.Version, "v")
		}
		if commit == "unknown" {
			var dirty bool
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					commit = setting.Value
				case "vcs.modified":
					dirty = setting.Value == "true"
				}
			}
			if dirty && commit != "unknown" {
				commit += "-dirty"
			}
		}
	}
	// VCS time describes a commit, not the build. Keep an unknown build date
	// when no date was supplied by the release process.
	return fmt.Sprintf("tclient %s (commit %s, built %s, %s %s/%s)",
		version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
