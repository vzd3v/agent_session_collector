package receive

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/shlex"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/diskspace"
)

const defaultRsyncPath = "/usr/bin/rsync"

type Request struct {
	SenderID        string
	OriginalCommand string
	DirectArgs      []string
	RsyncPath       string
}

type Plan struct {
	RsyncPath  string
	Args       []string
	MirrorPath string
	Sender     config.ReceiveSender
}

func PlanRsync(cfg config.HubAppConfig, req Request) (Plan, error) {
	sender, err := findSender(cfg, req.SenderID)
	if err != nil {
		return Plan{}, err
	}
	serverArgs, err := serverArgs(req)
	if err != nil {
		return Plan{}, err
	}
	if err := validateServerArgs(serverArgs); err != nil {
		return Plan{}, err
	}
	mirrorPath, err := senderMirrorPath(cfg, sender)
	if err != nil {
		return Plan{}, err
	}
	rsyncPath := req.RsyncPath
	if rsyncPath == "" {
		rsyncPath = defaultRsyncPath
	}
	options := append([]string{}, serverArgs[1:len(serverArgs)-2]...)
	args := []string{"--server", fmt.Sprintf("--max-size=%d", cfg.Receive.MaxFileSize.Bytes)}
	args = append(args, options...)
	args = append(args, ".", mirrorPath)
	return Plan{
		RsyncPath:  rsyncPath,
		Args:       args,
		MirrorPath: mirrorPath,
		Sender:     sender,
	}, nil
}

func Exec(cfg config.HubAppConfig, req Request, env []string) error {
	plan, err := PlanRsync(cfg, req)
	if err != nil {
		return err
	}
	if err := verifyCurrentIdentity(plan.Sender); err != nil {
		return err
	}
	if err := os.MkdirAll(plan.MirrorPath, 0o770); err != nil {
		return fmt.Errorf("create mirror path: %w", err)
	}
	if err := diskspace.Check(plan.MirrorPath, cfg.Retention.MinFreeBytes.Bytes, 0); err != nil {
		return fmt.Errorf("receive admission: %w", err)
	}
	argv := append([]string{plan.RsyncPath}, plan.Args...)
	return syscall.Exec(plan.RsyncPath, argv, env)
}

func findSender(cfg config.HubAppConfig, senderID string) (config.ReceiveSender, error) {
	for _, sender := range cfg.Receive.Sender {
		if sender.SenderID == senderID {
			return sender, nil
		}
	}
	return config.ReceiveSender{}, fmt.Errorf("unknown sender_id %q", senderID)
}

func serverArgs(req Request) ([]string, error) {
	if idx := indexString(req.DirectArgs, "--server"); idx >= 0 {
		return append([]string{}, req.DirectArgs[idx:]...), nil
	}
	if strings.TrimSpace(req.OriginalCommand) == "" {
		return nil, fmt.Errorf("SSH_ORIGINAL_COMMAND is empty and no direct --server args were provided")
	}
	words, err := shlex.Split(req.OriginalCommand)
	if err != nil {
		return nil, fmt.Errorf("parse SSH_ORIGINAL_COMMAND: %w", err)
	}
	idx := indexString(words, "--server")
	if idx < 0 {
		return nil, fmt.Errorf("SSH_ORIGINAL_COMMAND does not contain rsync --server mode")
	}
	return words[idx:], nil
}

func validateServerArgs(args []string) error {
	if len(args) < 5 {
		return fmt.Errorf("rsync server command is too short")
	}
	if args[0] != "--server" {
		return fmt.Errorf("rsync command must start with --server")
	}
	if args[len(args)-2] != "." {
		return fmt.Errorf("rsync command separator must be dot")
	}
	if args[len(args)-1] != "/" {
		return fmt.Errorf("rsync destination must be /")
	}
	var haveShort, haveRecursive, haveTimes, haveExtended, havePartial, haveSafeLinks bool
	for _, arg := range args[1 : len(args)-2] {
		switch {
		case arg == "--partial":
			havePartial = true
		case arg == "--safe-links":
			haveSafeLinks = true
		case strings.HasPrefix(arg, "--bwlimit="):
			if err := validatePositiveNumberOption(arg, "--bwlimit="); err != nil {
				return err
			}
		case strings.HasPrefix(arg, "--"):
			return fmt.Errorf("rsync option %q is not allowed", arg)
		case strings.HasPrefix(arg, "-"):
			if err := validateShortServerFlags(arg); err != nil {
				return err
			}
			haveRecursive = haveRecursive || strings.ContainsRune(arg, 'r')
			haveTimes = haveTimes || strings.ContainsRune(arg, 't')
			haveExtended = haveExtended || strings.ContainsRune(arg, 'e')
			haveShort = true
		default:
			return fmt.Errorf("unexpected rsync server argument %q", arg)
		}
	}
	if !haveShort {
		return fmt.Errorf("rsync server flags are missing")
	}
	if !haveRecursive || !haveTimes || !haveExtended {
		return fmt.Errorf("rsync server flags must include recursive, times, and extended protocol flags")
	}
	if !havePartial {
		return fmt.Errorf("rsync --partial is required")
	}
	if !haveSafeLinks {
		return fmt.Errorf("rsync --safe-links is required")
	}
	return nil
}

type processIdentity struct {
	username string
	groupIDs map[string]struct{}
}

func verifyCurrentIdentity(sender config.ReceiveSender) error {
	identity, err := currentProcessIdentity()
	if err != nil {
		return err
	}
	group, err := user.LookupGroup(sender.MirrorGroup)
	if err != nil {
		return fmt.Errorf("lookup mirror group %q: %w", sender.MirrorGroup, err)
	}
	return verifySenderIdentity(sender, identity, group.Gid)
}

func currentProcessIdentity() (processIdentity, error) {
	effectiveUser, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return processIdentity{}, fmt.Errorf("lookup effective user: %w", err)
	}
	gids, err := os.Getgroups()
	if err != nil {
		return processIdentity{}, fmt.Errorf("read process groups: %w", err)
	}
	gids = append(gids, os.Getegid())
	groupIDs := make(map[string]struct{}, len(gids))
	for _, gid := range gids {
		groupIDs[strconv.Itoa(gid)] = struct{}{}
	}
	return processIdentity{username: effectiveUser.Username, groupIDs: groupIDs}, nil
}

func verifySenderIdentity(sender config.ReceiveSender, identity processIdentity, mirrorGroupID string) error {
	if identity.username != sender.ReceiveUser {
		return fmt.Errorf("effective user %q does not match receive_user %q", identity.username, sender.ReceiveUser)
	}
	if _, ok := identity.groupIDs[mirrorGroupID]; !ok {
		return fmt.Errorf("effective user %q is not a member of mirror_group %q", identity.username, sender.MirrorGroup)
	}
	return nil
}

func validatePositiveNumberOption(arg, prefix string) error {
	value := strings.TrimPrefix(arg, prefix)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return fmt.Errorf("rsync option %q must be a positive integer", prefix)
	}
	return nil
}

func validateShortServerFlags(arg string) error {
	const allowed = "trRe.iLsfxCIvu"
	flags := strings.TrimPrefix(arg, "-")
	if flags == "" {
		return fmt.Errorf("rsync server flags are empty")
	}
	for _, r := range flags {
		if !strings.ContainsRune(allowed, r) {
			return fmt.Errorf("rsync server flag %q is not allowed", string(r))
		}
	}
	return nil
}

func senderMirrorPath(cfg config.HubAppConfig, sender config.ReceiveSender) (string, error) {
	root := filepath.Clean(cfg.Paths.Mirror)
	mirrorPath := filepath.Clean(filepath.Join(root, sender.Mirror))
	rel, err := filepath.Rel(root, mirrorPath)
	if err != nil {
		return "", fmt.Errorf("resolve sender mirror path: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("sender mirror path escapes paths.mirror")
	}
	return mirrorPath, nil
}

func indexString(values []string, needle string) int {
	for i, value := range values {
		if value == needle {
			return i
		}
	}
	return -1
}
