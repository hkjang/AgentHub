package buildinfo

// These fields are populated by -ldflags in release builds.
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
	// BaseVersion is the runtime base image this control plane expects. It is
	// tracked separately from Version because the base image is several GB and
	// is only rebuilt when something it is built from changes, so a control
	// plane release usually keeps running on an older base tag.
	BaseVersion = "0.1.0-dev"
)

type Info struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildTime   string `json:"buildTime"`
	BaseVersion string `json:"baseVersion"`
}

func Current() Info {
	return Info{Name: "AgentHub", Version: Version, Commit: Commit, BuildTime: BuildTime, BaseVersion: BaseVersion}
}
