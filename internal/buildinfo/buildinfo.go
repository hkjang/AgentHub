package buildinfo

// These fields are populated by -ldflags in release builds.
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

func Current() Info {
	return Info{Name: "AgentHub", Version: Version, Commit: Commit, BuildTime: BuildTime}
}
