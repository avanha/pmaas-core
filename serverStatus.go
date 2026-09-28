package core

import (
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-spi"
)

// collectPluginVersions resolves each plugin's build version once at startup (see
// moduleVersion) - version info never changes for the lifetime of the process, so there's no
// reason to redo this work on every request to "/".
func collectPluginVersions(wrappers []*plugins.PluginWrapper) []spi.PluginVersion {
	buildInfo, _ := debug.ReadBuildInfo()

	versions := make([]spi.PluginVersion, len(wrappers))

	for i, wrapper := range wrappers {
		versions[i] = spi.PluginVersion{
			ShortName: wrapper.Instance.ShortName(),
			Version:   moduleVersion(buildInfo, wrapper.PluginType),
		}
	}

	return versions
}

// moduleVersion looks up pluginType's own module in buildInfo's dependency list (falling back
// to a replace target, if any) and returns its Version - a semantic version tag if the
// module was fetched at one, a pseudo-version encoding its git commit hash/time if not, or
// "(devel)" for a module resolved from a local directory rather than a fixed version, which is
// what this project's own go.work-based development builds normally produce. Returns "" if
// buildInfo is nil (no embedded build info at all) or pluginType's module can't be found in it.
func moduleVersion(buildInfo *debug.BuildInfo, pluginType reflect.Type) string {
	if buildInfo == nil {
		return ""
	}

	pkgPath := pluginType.PkgPath()

	for _, dep := range buildInfo.Deps {
		module := dep
		if module.Replace != nil {
			module = module.Replace
		}

		if pkgPath == module.Path || strings.HasPrefix(pkgPath, module.Path+"/") {
			return module.Version
		}
	}

	return ""
}

// assemblyInfo derives the running assembly's display name/version from buildInfo's main
// module (the module of the assembly's own main package, e.g. pmaas-assembly-demo) - name is
// the last path segment of the main module's path, version is its Version (see
// spi.PluginVersion.Version for what that value can look like). Returns "", "" if buildInfo is
// nil (no embedded build info at all).
func assemblyInfo(buildInfo *debug.BuildInfo) (name string, version string) {
	if buildInfo == nil {
		return "", ""
	}

	name = buildInfo.Main.Path
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}

	return name, buildInfo.Main.Version
}

// readLoadAverage returns the host system's 1/5/15-minute load average by reading
// /proc/loadavg, or nil if that's not available - anywhere other than Linux (this project's
// actual deployment target), or if the file is unreadable/malformed for any reason. There's no
// portable way to get this outside Linux without a third-party dependency, which isn't worth
// pulling in just for a status page.
func readLoadAverage() *spi.LoadAverage {
	if runtime.GOOS != "linux" {
		return nil
	}

	contents, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}

	fields := strings.Fields(string(contents))
	if len(fields) < 3 {
		return nil
	}

	load1, err1 := strconv.ParseFloat(fields[0], 64)
	load5, err5 := strconv.ParseFloat(fields[1], 64)
	load15, err15 := strconv.ParseFloat(fields[2], 64)

	if err1 != nil || err5 != nil || err15 != nil {
		return nil
	}

	return &spi.LoadAverage{Load1: load1, Load5: load5, Load15: load15}
}

// readMemoryStats returns this process's own memory usage - see spi.MemoryStats.
func readMemoryStats() spi.MemoryStats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return spi.MemoryStats{
		AllocBytes:      memStats.Alloc,
		TotalAllocBytes: memStats.TotalAlloc,
		SysBytes:        memStats.Sys,
		NumGC:           memStats.NumGC,
	}
}
