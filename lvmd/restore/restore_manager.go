package restore

// BaseConf specifies where to find restore script
type BaseConf struct {
	// Path to shell script that downloads content of a storage backend into a newly created lvm volume
	ScriptPath string `json:"script-path"`
}
