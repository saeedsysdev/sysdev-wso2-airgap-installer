package version

const Current = "0.1.0-dev"

type Info struct {
	Application string `json:"application"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuiltAt     string `json:"built_at"`
	Dirty       string `json:"dirty"`
	Attribution string `json:"attribution"`
}
