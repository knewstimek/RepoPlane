package pathfacts

import "os"

type LinkFacts struct {
	Symlink       bool    `json:"symlink"`
	ReparsePoint  *bool   `json:"reparse_point"`
	Junction      *bool   `json:"junction"`
	HardlinkCount *uint64 `json:"hardlink_count"`
}

func observeLinks(path string, info os.FileInfo) LinkFacts {
	return platformLinks(path, info)
}
