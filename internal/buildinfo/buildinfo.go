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
	// LangflowVersion is the Langflow runtime image this control plane expects.
	// Langflow ships its own Python tree and frontend, so it is built and
	// published apart from the shared base image and moves on its own schedule.
	LangflowVersion = "0.1.0-dev"
	// QwenCodeVersion is the Qwen Code runtime image this control plane expects.
	// Like Langflow's, it is built and published apart from the shared base image.
	QwenCodeVersion = "0.1.0-dev"
	// JupyterVersion, NodeREDVersion and N8NVersion are the runtime images for the
	// products that do not boot from the shared one either. Each is published on
	// its own schedule and only the sites that use it have to carry it.
	JupyterVersion = "0.1.0-dev"
	NodeREDVersion = "0.1.0-dev"
	N8NVersion     = "0.1.0-dev"
	GooseVersion   = "0.1.0-dev"
	HolmesVersion  = "0.1.0-dev"
	// BrowserCodeVersion is the image that carries the browser-driving agent.
	BrowserCodeVersion = "0.1.0-dev"
)

type Info struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	BuildTime          string `json:"buildTime"`
	BaseVersion        string `json:"baseVersion"`
	LangflowVersion    string `json:"langflowVersion"`
	QwenCodeVersion    string `json:"qwenCodeVersion"`
	JupyterVersion     string `json:"jupyterVersion"`
	NodeREDVersion     string `json:"nodeRedVersion"`
	N8NVersion         string `json:"n8nVersion"`
	GooseVersion       string `json:"gooseVersion"`
	HolmesVersion      string `json:"holmesVersion"`
	BrowserCodeVersion string `json:"browserCodeVersion"`
}

func Current() Info {
	return Info{Name: "AgentHub", Version: Version, Commit: Commit, BuildTime: BuildTime, BaseVersion: BaseVersion, LangflowVersion: LangflowVersion, QwenCodeVersion: QwenCodeVersion,
		JupyterVersion: JupyterVersion, NodeREDVersion: NodeREDVersion, N8NVersion: N8NVersion, GooseVersion: GooseVersion, HolmesVersion: HolmesVersion, BrowserCodeVersion: BrowserCodeVersion}
}
