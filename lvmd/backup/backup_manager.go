package backup

// BaseConf specifies where to find backup script
type BaseConf struct {
	// Path to shell script that creates lvm snapshot and uploads its content to a storage
	ScriptPath string `json:"script-path"`
	// Workdir specifies where to look for state information, as backup process is triggered async
	Workdir string `json:"workdir"`
}
