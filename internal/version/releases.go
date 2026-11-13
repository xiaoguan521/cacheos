package version

// Release describes the auditable product-to-upstream mapping shown in the
// admin About page. The dataplane build writes the same values to BUILDINFO
// and SBOM; keeping the small release index here makes the mapping available
// to the console without reading a source-tree-relative file at runtime.
type Release struct {
	Product        string   `json:"product"`
	Upstream       string   `json:"upstream"`
	UpstreamCommit string   `json:"upstream_commit"`
	Patches        []string `json:"patches"`
	CVEs           []string `json:"cves"`
}

var Releases = []Release{
	{
		Product:        "0.1.1",
		Upstream:       "valkey 9.1.3",
		UpstreamCommit: "29173b67508b2de4c0f0ed551f3b47cf19fc3ca8",
		Patches:        []string{"0001-sycache-rename.patch"},
		CVEs:           []string{},
	},
	{
		Product:        "0.1.0",
		Upstream:       "valkey 9.1.3",
		UpstreamCommit: "29173b67508b2de4c0f0ed551f3b47cf19fc3ca8",
		Patches:        []string{"0001-sycache-rename.patch"},
		CVEs:           []string{},
	},
	{
		Product:        "1.0.0-dev",
		Upstream:       "valkey 9.1.3",
		UpstreamCommit: "29173b67508b2de4c0f0ed551f3b47cf19fc3ca8",
		Patches:        []string{"0001-sycache-rename.patch"},
		CVEs:           []string{},
	},
}
