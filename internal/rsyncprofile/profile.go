package rsyncprofile

import "strconv"

type Profile struct {
	RsyncPath      string
	SSHTarget      string
	RemoteCommand  string
	BandwidthLimit string
}

func SenderArgs(p Profile, sourceDir string) []string {
	remoteCommand := p.RemoteCommand
	if remoteCommand == "" {
		remoteCommand = "asc-receive"
	}

	args := []string{
		"-rt",
		"--safe-links",
		"--no-links",
		"--no-perms",
		"--no-owner",
		"--no-group",
		"--no-devices",
		"--no-specials",
		"--chmod=Du=rwx,Dg=rwx,Do=,Fu=rw,Fg=rw,Fo=",
		"--partial",
		"--rsync-path=" + strconv.Quote(remoteCommand),
	}
	if p.BandwidthLimit != "" {
		args = append(args, "--bwlimit="+p.BandwidthLimit)
	}
	return append(args, sourceDir+"/", p.SSHTarget+":/")
}

func SenderCommand(p Profile) string {
	if p.RsyncPath != "" {
		return p.RsyncPath
	}
	return "/usr/bin/rsync"
}
